package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

// prepareWAABackend prepares browser dependencies according to the WAA backend; the Go backend only prepares Camoufox on demand during account login, using proxy when downloading.
func prepareWAABackend(ctx context.Context, cfg config.Config, requests *requestRegistry, accounts int, proxy string) (string, aistudio.IsolatedLoginDriver, error) {
	if cfg.WAABackend == config.WAABackendGo {
		requests.log("service", "INFO", fmt.Sprintf("Runtime assembly | 2/3 | WAA backend=go | accounts=%d", accounts))
		return "", &lazyLoginDriver{timeout: cfg.RequestTimeout, proxy: proxy}, nil
	}
	requests.log("service", "INFO", fmt.Sprintf("Runtime assembly | 2/3 | Verifying Camoufox | accounts=%d", accounts))
	camoufoxPath, err := camoufoxnative.FindExecutable(ctx, proxy)
	if err != nil {
		return "", nil, err
	}
	removeStaleCamoufoxProfiles(requests)
	login, err := aistudio.NewNativeLoginDriver(camoufoxPath, cfg.RequestTimeout)
	if err != nil {
		return "", nil, err
	}
	return camoufoxPath, login, nil
}

// removeStaleCamoufoxProfiles cleans up Camoufox profiles left behind by previous processes
func removeStaleCamoufoxProfiles(requests *requestRegistry) {
	if removed, err := camoufoxnative.RemoveStaleProfiles(); err != nil {
		requests.log("service", "WARN", fmt.Sprintf(
			"Stale Camoufox profile cleanup failed | removed=%d | error=%s", removed, strings.TrimSpace(err.Error()),
		))
	} else if removed > 0 {
		requests.log("service", "INFO", fmt.Sprintf("Stale Camoufox profiles cleaned up | count=%d", removed))
	}
}

// newWAAWorker starts an account WAA worker; if the worker config has no Camoufox path, the pure Go VM handles it
func newWAAWorker(ctx context.Context, accountID string, options camoufoxnative.Options) (*aistudio.NativeWorker, error) {
	if options.ExecutablePath == "" {
		return aistudio.NewGoWorker(ctx, accountID, options)
	}
	return aistudio.NewNativeWorker(ctx, accountID, options)
}

// lazyLoginDriver locates Camoufox and creates a login driver upon first login or account verification
type lazyLoginDriver struct {
	timeout time.Duration
	proxy   string
	mu      sync.Mutex
	driver  *aistudio.NativeLoginDriver
}

func (lazy *lazyLoginDriver) resolve(ctx context.Context) (*aistudio.NativeLoginDriver, error) {
	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	if lazy.driver != nil {
		return lazy.driver, nil
	}
	camoufoxPath, err := camoufoxnative.FindExecutable(ctx, lazy.proxy)
	if err != nil {
		return nil, err
	}
	driver, err := aistudio.NewNativeLoginDriver(camoufoxPath, lazy.timeout)
	if err != nil {
		return nil, err
	}
	lazy.driver = driver
	return driver, nil
}

// Login executes isolated login using on-demand Camoufox
func (lazy *lazyLoginDriver) Login(ctx context.Context, request aistudio.IsolatedLoginRequest) (aistudio.IsolatedLoginResult, error) {
	driver, err := lazy.resolve(ctx)
	if err != nil {
		return aistudio.IsolatedLoginResult{}, err
	}
	return driver.Login(ctx, request)
}

