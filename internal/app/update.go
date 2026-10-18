package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/minio/selfupdate"
	"golang.org/x/mod/semver"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

// releaseAPI is the GitHub latest release endpoint.
const releaseAPI = "https://api.github.com/repos/Mag1cFall/AIStudio2API/releases/latest"

// releaseCheckInterval is the cache duration for latest release checks.
const releaseCheckInterval = time.Hour

// githubRelease contains fields used from GitHub releases API.
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

// githubAsset represents an asset in a release.
type githubAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

// updateConflictError indicates updates cannot be installed in the current state.
type updateConflictError string

func (err updateConflictError) Error() string { return string(err) }

// HTTPStatus returns HTTP status code for update conflicts.
func (updateConflictError) HTTPStatus() int { return http.StatusConflict }

// selfUpdater checks for latest releases and replaces the current executable.
type selfUpdater struct {
	version    string
	mode       string
	releaseURL string
	executable string
	proxy      func() string
	log        func(level, message string)
	restart    func()
	mu         sync.Mutex
	release    *githubRelease
	checked    time.Time
	checkErr   error
	updating   bool
}

// newSelfUpdater creates an updater for executable based on build version and runtime environment.
func newSelfUpdater(version, executable string, proxy func() string, log func(level, message string), restart func()) *selfUpdater {
	return &selfUpdater{
		version: version, mode: updateMode(version), releaseURL: releaseAPI, executable: executable,
		proxy: proxy, log: log, restart: restart,
	}
}

// updateMode returns the update mode: binary can update in-place, container updates with image, development for source builds.
func updateMode(version string) string {
	if !semver.IsValid(version) || semver.Prerelease(version) != "" || semver.Build(version) != "" {
		return "development"
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return "container"
		}
	}
	return "binary"
}

// UpdateStatus returns current version and latest release, re-fetching when refresh is true or cache expired.
func (u *selfUpdater) UpdateStatus(ctx context.Context, refresh bool) (api.UpdateStatus, error) {
	u.mu.Lock()
	stale := refresh || time.Since(u.checked) >= releaseCheckInterval
	u.mu.Unlock()
	if stale {
		release, err := u.fetchRelease(ctx)
		if ctx.Err() != nil {
			return api.UpdateStatus{}, ctx.Err()
		}
		u.mu.Lock()
		u.checked, u.checkErr = time.Now(), err
		if err == nil {
			u.release = release
		}
		u.mu.Unlock()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked(), nil
}

// ApplyUpdate downloads the latest release archive, verifies digest, replaces current binary, and relaunches manager.
func (u *selfUpdater) ApplyUpdate(ctx context.Context) (api.UpdateStatus, error) {
	u.mu.Lock()
	if u.mode != "binary" {
		u.mu.Unlock()
		return api.UpdateStatus{}, updateConflictError("current runtime mode does not support online updates")
	}
	if u.updating {
		u.mu.Unlock()
		return api.UpdateStatus{}, updateConflictError("update is already in progress")
	}
	u.updating = true
	u.mu.Unlock()
	release, err := u.fetchRelease(ctx)
	if err == nil && semver.Compare(release.TagName, u.version) <= 0 {
		err = updateConflictError("already on latest version " + u.version)
	}
	if err == nil {
		u.log("INFO", "Online update | 1/2 | Downloading release package | version="+release.TagName)
		err = u.install(ctx, release)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.updating = false
		u.log("ERROR", "Online update failed | error="+err.Error())
		return api.UpdateStatus{}, err
	}
	u.release, u.checked, u.checkErr = release, time.Now(), nil
		u.log("INFO", "Online update complete | version="+release.TagName)
	status := u.statusLocked()
	u.restart()
	return status, nil
}

// statusLocked builds update status while holding lock.
func (u *selfUpdater) statusLocked() api.UpdateStatus {
	status := api.UpdateStatus{Current: u.version, Mode: u.mode, CheckedAt: u.checked, Updating: u.updating}
	if u.checkErr != nil {
		status.Error = u.checkErr.Error()
	}
	if release := u.release; release != nil {
		status.Latest, status.Notes, status.URL, status.PublishedAt = release.TagName, release.Body, release.HTMLURL, release.PublishedAt
		status.Available = semver.Compare(release.TagName, u.version) > 0
	}
	return status
}

// fetchRelease queries GitHub for the latest release.
func (u *selfUpdater) fetchRelease(ctx context.Context) (*githubRelease, error) {
	response, err := u.get(ctx, u.releaseURL, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer response.Body.Close()
	var release githubRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("parse latest release: %w", err)
	}
	return &release, nil
}

// install downloads release archive for current platform/arch, verifies SHA-256, and replaces binary.
func (u *selfUpdater) install(ctx context.Context, release *githubRelease) error {
	name, binary := releasePackage(release.TagName)
	index := slices.IndexFunc(release.Assets, func(asset githubAsset) bool { return asset.Name == name })
	if index < 0 {
		return fmt.Errorf("release %s has no asset %s", release.TagName, name)
	}
	asset := release.Assets[index]
	expected, ok := strings.CutPrefix(asset.Digest, "sha256:")
	if !ok {
		return fmt.Errorf("release asset %s missing SHA-256 digest", name)
	}
	response, err := u.get(ctx, asset.URL, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	archive, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != expected {
		return fmt.Errorf("release archive %s SHA-256 digest does not match release record", name)
	}
	executable, err := extractExecutable(archive, name, binary)
	if err != nil {
		return err
	}
	u.log("INFO", "Online update | 2/2 | Replacing executable | version="+release.TagName)
	if err := selfupdate.Apply(bytes.NewReader(executable), selfupdate.Options{TargetPath: u.executable}); err != nil {
		if rollback := selfupdate.RollbackError(err); rollback != nil {
			return fmt.Errorf("replace executable: %w; rollback failed: %v", err, rollback)
		}
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}

// get sends GET via manager egress proxy, returning error on non-200 responses.
func (u *selfUpdater) get(ctx context.Context, address, accept string) (*http.Response, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy := u.proxy(); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("parse proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		return nil, fmt.Errorf("HTTP %d %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return response, nil
}

// releasePackage returns package filename and executable filename for current OS and architecture.
func releasePackage(tag string) (string, string) {
	base := fmt.Sprintf("aistudio2api-%s-%s-%s", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		return base + ".zip", "aistudio2api.exe"
	}
	return base + ".tar.gz", "aistudio2api"
}

// extractExecutable extracts executable binary from zip or tar.gz archive.
func extractExecutable(archive []byte, name, binary string) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		for _, file := range reader.File {
			if path.Base(file.Name) != binary {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("read release archive: %w", err)
			}
			defer opened.Close()
			return io.ReadAll(opened)
		}
		return nil, fmt.Errorf("release archive missing %s", binary)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read release archive: %w", err)
	}
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("release archive missing %s", binary)
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == binary {
			return io.ReadAll(reader)
		}
	}
}

// relaunch launches replaced executable with original arguments: Windows keeps current console, other systems exec in-place.
func relaunch(executable string, args []string) error {
	args = append(slices.Clone(args), "-open-ui=false")
	if runtime.GOOS == "windows" {
		command := exec.Command(executable, args...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		return command.Start()
	}
	return syscall.Exec(executable, append([]string{executable}, args...), os.Environ())
}
