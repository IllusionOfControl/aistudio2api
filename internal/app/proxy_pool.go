package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

// proxyProbeInterval is the interval for scheduled health checks of the proxy pool.
const proxyProbeInterval = 5 * time.Minute

// proxyProbeTarget is the URL requested during proxy health checks.
const proxyProbeTarget = "https://aistudio.google.com/"

// checkProxy accesses AI Studio via proxy, considering it healthy upon receiving any HTTP response.
func checkProxy(ctx context.Context, proxy string) error {
	client, err := aistudio.NewProxyHTTPClient(proxy)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyProbeTarget, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	return response.Body.Close()
}

// proxyNetworkError checks whether request failure was caused by a proxy network error.
func proxyNetworkError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var transportError *url.Error
	return errors.As(err, &transportError) || errors.Is(err, camoufoxnative.ErrProxyUnreachable) || strings.Contains(err.Error(), "NetworkError when attempting to fetch resource")
}

// reportProxyFailure records network errors through proxy, rebinding accounts when proxy is marked unavailable.
func (service *trackedService) reportProxyFailure(proxy string, label string, err error) {
	changed, proxyErr := service.proxies.ReportFailure(proxy, err)
	if proxyErr != nil {
		service.requests.log(label, "WARN", "Proxy rebind | error="+proxyErr.Error())
	}
	if len(changed) > 0 && service.rebindAccounts != nil {
		service.rebindAccounts(changed)
	}
}

// probeProxies periodically probes the proxy pool and rebinds accounts on unavailable proxies.
func (admin *runtimeAdmin) probeProxies(ctx context.Context) {
	ticker := time.NewTicker(proxyProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		changed, err := admin.proxies.Probe(ctx, checkProxy)
		if err != nil && ctx.Err() == nil {
			admin.requests.log("service", "WARN", "Proxy rebind | error="+err.Error())
		}
		admin.rebindAccounts(changed)
	}
}

// rebindAccounts rebuilds account header clients and workers in the background per new proxy pool bindings.
func (admin *runtimeAdmin) rebindAccounts(accountIDs []string) {
	if len(accountIDs) == 0 {
		return
	}
	go func() {
		for _, accountID := range accountIDs {
			if err := admin.rebindAccount(admin.lifecycle, accountID); err != nil && admin.lifecycle.Err() == nil {
				admin.requests.log(accountID, "WARN", "Proxy rebind | error="+err.Error())
			}
		}
	}()
}

func (admin *runtimeAdmin) rebindAccount(ctx context.Context, accountID string) error {
	lease, err := admin.pool.AcquireAccount(ctx, accountID)
	if err != nil {
		return err
	}
	account := lease.Account()
	headerUpdate, err := admin.headers.prepareUpdate(account, account.Config)
	if err != nil {
		return errors.Join(err, lease.Release())
	}
	workerUpdate, err := admin.workers.prepareUpdate(account, account.Config)
	if err != nil {
		headerUpdate.Discard()
		return errors.Join(err, lease.Release())
	}
	workerUpdate.Commit()
	headerUpdate.Commit()
	admin.requests.log(account.Config.Label, "WARN", fmt.Sprintf("Proxy rebind | egress=%s", redactedProxy(account.EffectiveProxy(admin.globalProxy))))
	return lease.Release()
}
