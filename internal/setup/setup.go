package setup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
	"github.com/Mag1cFall/AIStudio2API/internal/chromeauth"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

type setupStrings []string

func (values *setupStrings) String() string {
	return strings.Join(*values, ",")
}

func (values *setupStrings) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}

	*values = append(*values, value)
	return nil
}

type setupOptions struct {
	storageState string
	login        bool
	chromeRoot   string
	profiles     setupStrings
	emails       setupStrings
	proxy        string
	locale       string
	localeSet    bool
	timezone     string
	remote       string
	token        string
}

// accountTarget saves an account obtained during setup and returns the account name.
type accountTarget interface {
	Save(ctx context.Context, accountConfig aistudio.AccountConfig, state aistudio.StorageState, fingerprintDirectory string) (string, error)
}

// Run executes local Chrome import, file import, or isolated login, writing results to the local account directory or a remote instance.
func Run(ctx context.Context, cfg config.Config, args []string) error {
	options, err := parseSetupFlags(args, cfg)
	if err != nil {
		return err
	}
	input := bufio.NewReader(os.Stdin)
	var target accountTarget
	if options.remote != "" {
		token := options.token
		if token == "" {
			if token, err = readPairingToken(input, os.Stdout); err != nil {
				return err
			}
		}
		target = remoteTarget{
			client: &http.Client{Timeout: cfg.RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}},
			origin: options.remote, token: token,
		}
	} else {
		if err := validateSetupRoot(cfg.AuthStates); err != nil {
			return err
		}
		target = localTarget{store: aistudio.NewAccountStore(cfg.AuthStates)}
	}
	if options.storageState != "" {
		return importStorageState(ctx, target, options)
	}

	if options.login {
		driver, err := defaultSetupLoginDriver(ctx, cfg)
		if err != nil {
			return err
		}
		return importIsolatedLogin(ctx, target, options, driver)
	}
	return importChromeAccounts(ctx, cfg, target, options, input, os.Stdout)
}

// localTarget writes account to local account directory
type localTarget struct {
	store *aistudio.AccountStore
}

// Save creates local account directory and persists isolated login fingerprint
func (target localTarget) Save(
	_ context.Context,
	accountConfig aistudio.AccountConfig,
	state aistudio.StorageState,
	fingerprintDirectory string,
) (string, error) {
	account, publishLease, err := target.store.Create(accountConfig, state)
	if err != nil {
		return "", err
	}
	if fingerprintDirectory != "" {
		if err := camoufoxnative.PersistAccountFingerprint(fingerprintDirectory, account.Directory); err != nil {
			return "", errors.Join(err, target.store.Delete(account), publishLease.Release())
		}
	}
	return account.Config.Label, publishLease.Release()
}

// remoteTarget uploads account to remote instance using pairing token
type remoteTarget struct {
	client *http.Client
	origin string
	token  string
}

// Save uploads authentication state and isolated login fingerprint
func (target remoteTarget) Save(
	ctx context.Context,
	accountConfig aistudio.AccountConfig,
	state aistudio.StorageState,
	fingerprintDirectory string,
) (string, error) {
	input := api.AccountStateInput{
		Label: accountConfig.Label, Locale: accountConfig.Locale, Timezone: accountConfig.Timezone, StorageState: state,
	}
	if fingerprintDirectory != "" {
		data, err := os.ReadFile(filepath.Join(fingerprintDirectory, "camoufox-fingerprint.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read Camoufox fingerprint: %w", err)
		}
		input.Fingerprint = data
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode account: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.origin+"/api/pairing/accounts", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+target.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := target.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("connect to remote instance: %w", err)
	}
	defer response.Body.Close()
	var result struct {
		Account api.AdminAccount `json:"account"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	if response.StatusCode != http.StatusCreated {
		if result.Error.Message != "" {
			return "", fmt.Errorf("remote import failed: HTTP %d: %s", response.StatusCode, result.Error.Message)
		}
		return "", fmt.Errorf("remote import failed: HTTP %d", response.StatusCode)
	}
	if decodeErr != nil {
		return "", fmt.Errorf("decode remote instance response: %w", decodeErr)
	}
	return result.Account.Label, nil
}

// readPairingToken reads pairing token from standard input, keeping it out of shell history
func readPairingToken(input *bufio.Reader, output io.Writer) (string, error) {
	fmt.Fprint(output, "Paste pairing token generated on remote instance accounts page: ")
	line, err := input.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read pairing token: %w", err)
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", fmt.Errorf("pairing token cannot be empty")
	}
	return token, nil
}

// remoteOrigin validates remote instance address has scheme and host only; non-loopback must use HTTPS
func remoteOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("--remote must be origin of remote instance, e.g. https://example.com")
	}
	ip := net.ParseIP(parsed.Hostname())
	loopback := strings.EqualFold(parsed.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback) {
		return "", fmt.Errorf("--remote must use HTTPS; HTTP is only allowed for loopback")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func importStorageState(ctx context.Context, target accountTarget, options setupOptions) error {
	state, err := aistudio.LoadStorageState(options.storageState)
	if err != nil {
		return err
	}

	if _, err := aistudio.NewSigner().Sign(state); err != nil {
		return fmt.Errorf("storage state cannot be used with AI Studio: %w", err)
	}

	label := defaultSetupLabel(options.storageState)
	if extension, exists, err := state.AuthExtension(); err != nil {
		return err
	} else if exists && strings.TrimSpace(extension.Source.Email) != "" {
		label = extension.Source.Email
	}
	saved, err := target.Save(ctx, setupAccountConfig(label, options), state, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Account saved: %s\n", saved)
	return nil
}

func importIsolatedLogin(
	ctx context.Context,
	target accountTarget,
	options setupOptions,
	driver aistudio.IsolatedLoginDriver,
) error {
	loginDirectory, err := os.MkdirTemp("", "aistudio2api-login-*")
	if err != nil {
		return fmt.Errorf("create isolated login directory: %w", err)
	}
	defer os.RemoveAll(loginDirectory)

	result, err := driver.Login(ctx, aistudio.IsolatedLoginRequest{
		AccountID: "setup",
		Directory: loginDirectory,
		Proxy:     options.proxy,
		Locale:    options.locale,
		Timezone:  options.timezone,
	})
	if err != nil {
		return err
	}

	if _, err := aistudio.NewSigner().Sign(result.StorageState); err != nil {
		return fmt.Errorf("storage state cannot be used with AI Studio: %w", err)
	}
	saved, err := target.Save(ctx, setupAccountConfig(result.Email, options), result.StorageState, loginDirectory)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Account saved: %s\n", saved)
	if result.DriveError != "" {
		fmt.Fprintf(os.Stdout, "Drive authorization failed: %s\n", result.DriveError)
	}
	return nil
}

func importChromeAccounts(ctx context.Context, cfg config.Config, target accountTarget, options setupOptions, input io.Reader, output io.Writer) error {
	root := options.chromeRoot
	if root == "" {
		var err error
		root, err = chromeauth.DefaultChromeRoot()
		if err != nil {
			return err
		}
	}
	var accountIDs []string
	if len(options.profiles) == 0 && len(options.emails) == 0 {
		accounts, err := chromeauth.Discover(root)
		if err != nil {
			return err
		}
		accountIDs, err = promptChromeAccounts(accounts, input, output)
		if err != nil {
			return err
		}
	}

	results, err := chromeauth.Import(ctx, chromeauth.ImportOptions{
		ChromeRoot: root,
		Proxy:      options.proxy,
		Profiles:   options.profiles,
		Emails:     options.emails,
		AccountIDs: accountIDs,
	})
	if err != nil {
		return err
	}

	modelCounts := make([]int, len(results))
	for index := range results {
		verifyContext, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		verification, verifyErr := chromeauth.Verify(verifyContext, &results[index].State, options.proxy)
		cancel()

		if verifyErr != nil {
			return fmt.Errorf("verify %s: %w", results[index].Email, verifyErr)
		}

		modelCounts[index] = verification.ModelCount
	}

	for index, result := range results {
		accountOptions := options
		if !options.localeSet && result.Locale != "" {
			accountOptions.locale = result.Locale
		}
		if _, err := target.Save(ctx, setupAccountConfig(result.Email, accountOptions), result.State, ""); err != nil {
			return err
		}

		fmt.Fprintf(output, "Imported: %s (%s), %d models\n", result.Email, result.Profile, modelCounts[index])
	}

	return nil
}

func promptChromeAccounts(accounts []chromeauth.Account, input io.Reader, output io.Writer) ([]string, error) {
	available := make([]chromeauth.Account, 0, len(accounts))
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)

	fmt.Fprintln(table, "No.\tStatus\tProfile\tDisplay Name\tEmail")
	for _, account := range accounts {
		index := "-"
		status := "Not Importable"
		if account.Importable {
			available = append(available, account)
			index = strconv.Itoa(len(available))
			status = "Importable"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", index, status, account.Profile, account.DisplayName, account.Email)
	}

	if err := table.Flush(); err != nil {
		return nil, fmt.Errorf("write Chrome account list: %w", err)
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no importable accounts found in local Chrome")
	}

	fmt.Fprint(output, "Enter comma-separated numbers: ")
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read account selection: %w", err)
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("no accounts selected")
	}
	selected := make([]string, 0)
	seen := make(map[int]struct{})

	for _, raw := range strings.Split(line, ",") {
		index, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || index < 1 || index > len(available) {
			return nil, fmt.Errorf("invalid account number %q", strings.TrimSpace(raw))
		}

		if _, exists := seen[index]; exists {
			continue
		}

		seen[index] = struct{}{}
		selected = append(selected, available[index-1].ID)
	}

	return selected, nil
}

func parseSetupFlags(args []string, cfg config.Config) (setupOptions, error) {
	flags := flag.NewFlagSet("aistudio2api setup", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Chrome import:   aistudio2api setup")
		fmt.Fprintln(flags.Output(), "File import:     aistudio2api setup --storage-state <file>")
		fmt.Fprintln(flags.Output(), "Isolated login:  aistudio2api setup --login")
		fmt.Fprintln(flags.Output(), "Remote instance: add --remote <origin> to any of the above commands")
		flags.PrintDefaults()
	}

	var profiles setupStrings
	var emails setupStrings
	flags.Var(&profiles, "profile", "Chrome profile to import, can be repeated")
	flags.Var(&emails, "email", "Google email to import, can be repeated")
	storageState := flags.String("storage-state", "", "Playwright storage state file")
	login := flags.Bool("login", false, "Log in using isolated Camoufox browser")
	chromeRoot := flags.String("chrome-root", "", "Chrome User Data directory")
	proxy := flags.String("proxy", cfg.Proxy, "Fixed HTTP, HTTPS, or SOCKS5 proxy for accounts")
	locale := flags.String("locale", aistudio.DefaultAccountLocale(), "Account locale")
	timezone := flags.String("timezone", aistudio.DefaultAccountTimezone(), "Account timezone")
	remote := flags.String("remote", "", "Remote instance origin, accounts are uploaded to this instance")
	token := flags.String("token", "", "Pairing token generated on remote instance accounts page; read from stdin if omitted")
	if err := flags.Parse(args); err != nil {
		return setupOptions{}, err
	}

	if flags.NArg() != 0 {
		return setupOptions{}, fmt.Errorf("unknown argument %q", flags.Arg(0))
	}

	options := setupOptions{
		storageState: strings.TrimSpace(*storageState), login: *login,
		chromeRoot: strings.TrimSpace(*chromeRoot), profiles: profiles, emails: emails,
		proxy:  strings.TrimSpace(*proxy),
		locale: strings.TrimSpace(*locale), timezone: strings.TrimSpace(*timezone),
		token: strings.TrimSpace(*token),
	}
	if strings.TrimSpace(*remote) != "" {
		origin, err := remoteOrigin(*remote)
		if err != nil {
			return setupOptions{}, err
		}
		options.remote = origin
	} else if options.token != "" {
		return setupOptions{}, fmt.Errorf("--token must be used together with --remote")
	}

	flags.Visit(func(value *flag.Flag) {
		if value.Name == "locale" {
			options.localeSet = true
		}
	})

	chromeSelection := options.chromeRoot != "" || len(options.profiles) != 0 || len(options.emails) != 0
	if options.storageState != "" && (options.login || chromeSelection) {
		return setupOptions{}, fmt.Errorf("--storage-state cannot be used together with Chrome import or --login")
	}

	if options.login && chromeSelection {
		return setupOptions{}, fmt.Errorf("--login cannot be used together with Chrome import flags")
	}

	if options.locale == "" || options.timezone == "" {
		return setupOptions{}, fmt.Errorf("locale and timezone cannot be empty")
	}

	if err := config.ValidateProxy(options.proxy); err != nil {
		return setupOptions{}, err
	}

	return options, nil
}

func setupAccountConfig(label string, options setupOptions) aistudio.AccountConfig {
	accountConfig := aistudio.DefaultAccountConfig(label)
	accountConfig.Proxy = options.proxy
	accountConfig.Locale = options.locale
	accountConfig.Timezone = options.timezone

	return accountConfig
}

func defaultSetupLabel(storageState string) string {
	parent := filepath.Base(filepath.Clean(filepath.Dir(storageState)))
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return "Default"
	}

	return parent
}

func defaultSetupLoginDriver(ctx context.Context, cfg config.Config) (aistudio.IsolatedLoginDriver, error) {
	camoufoxPath, err := camoufoxnative.FindExecutable(ctx)
	if err != nil {
		return nil, err
	}

	return aistudio.NewNativeLoginDriver(camoufoxPath, cfg.RequestTimeout)
}

func validateSetupRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("AISTUDIO_AUTH_STATES cannot be empty")
	}

	if strings.Contains(root, ",") {
		return fmt.Errorf("setup requires AISTUDIO_AUTH_STATES to point to a single account directory")
	}

	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read account directory: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("setup requires AISTUDIO_AUTH_STATES to point to an account directory")
	}

	return nil
}
