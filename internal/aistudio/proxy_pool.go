package aistudio

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// proxyFailureThreshold is the number of consecutive network errors allowed before marking a proxy as unavailable.
const proxyFailureThreshold = 3

// ProxyPool distributes accounts without dedicated proxies across a proxy pool by bound count, rebinding to available proxies on failure.
type ProxyPool struct {
	mu      sync.Mutex
	pool    *AccountPool
	entries []*proxyEntry
}

// proxyEntry represents a proxy in the proxy pool along with its health status.
type proxyEntry struct {
	proxy     string
	down      bool
	failures  int
	checkedAt time.Time
	lastError string
}

// ProxyStatus represents the availability and bound account count of a proxy in the proxy pool.
type ProxyStatus struct {
	Proxy     string     `json:"proxy"`
	Available bool       `json:"available"`
	Accounts  int        `json:"accounts"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// NewProxyPool creates a proxy pool; when proxies is empty, accounts use system proxy or direct connection.
func NewProxyPool(pool *AccountPool, proxies []string) *ProxyPool {
	entries := make([]*proxyEntry, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			entries = append(entries, &proxyEntry{proxy: proxy})
		}
	}
	return &ProxyPool{pool: pool, entries: entries}
}

// Assign binds proxies to accounts without dedicated proxies: retains existing bindings to available proxies, binds remaining accounts to available proxies with the fewest accounts, and writes to runtime state, returning accounts with changed bindings.
func (p *ProxyPool) Assign() ([]string, error) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.assignLocked()
}

func (p *ProxyPool) assignLocked() ([]string, error) {
	if len(p.entries) == 0 {
		return nil, nil
	}
	available := p.availableLocked()
	p.pool.mu.Lock()
	accounts := slices.Clone(p.pool.accounts)
	p.pool.mu.Unlock()
	counts := make(map[string]int, len(p.entries))
	bindings := make(map[*Account]string, len(accounts))
	pending := make([]*Account, 0, len(accounts))
	for _, account := range accounts {
		if strings.TrimSpace(account.Config.Proxy) != "" {
			continue
		}
		account.runtimeMu.Lock()
		bound := account.runtime.Proxy
		account.runtimeMu.Unlock()
		if current := account.boundProxy.Load(); bound == "" && current != nil {
			bound = *current
		}
		if slices.Contains(available, bound) {
			counts[bound]++
			bindings[account] = bound
			continue
		}
		pending = append(pending, account)
	}
	for _, account := range pending {
		chosen := available[0]
		for _, proxy := range available[1:] {
			if counts[proxy] < counts[chosen] {
				chosen = proxy
			}
		}
		counts[chosen]++
		bindings[account] = chosen
	}
	var changed []string
	var errs []error
	for _, account := range accounts {
		chosen, ok := bindings[account]
		if !ok {
			continue
		}
		previous := account.boundProxy.Load()
		_, err := p.pool.updateRuntime(account.ID, func(_ *Account, runtimeState *accountRuntimeState) (bool, func(*Account), error) {
			if runtimeState.Proxy == chosen {
				return false, nil, nil
			}
			runtimeState.Proxy = chosen
			return true, nil, nil
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		account.boundProxy.Store(&chosen)
		if previous != nil && *previous != chosen {
			changed = append(changed, account.ID)
		}
	}
	return changed, errors.Join(errs...)
}

// Next returns the proxy to bind to the next new account, or empty string when proxy pool is empty.
func (p *ProxyPool) Next() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) == 0 {
		return ""
	}
	available := p.availableLocked()
	counts := make(map[string]int, len(available))
	p.pool.mu.Lock()
	for _, account := range p.pool.accounts {
		if bound := account.boundProxy.Load(); bound != nil && strings.TrimSpace(account.Config.Proxy) == "" {
			counts[*bound]++
		}
	}
	p.pool.mu.Unlock()
	chosen := available[0]
	for _, proxy := range available[1:] {
		if counts[proxy] < counts[chosen] {
			chosen = proxy
		}
	}
	return chosen
}

// availableLocked returns available proxies, or all proxies if none are available.
func (p *ProxyPool) availableLocked() []string {
	var available, all []string
	for _, entry := range p.entries {
		all = append(all, entry.proxy)
		if !entry.down {
			available = append(available, entry.proxy)
		}
	}
	if len(available) == 0 {
		return all
	}
	return available
}

// Binding returns the account's currently bound proxy pool proxy, or empty string if account has a dedicated proxy or is unbound.
func (p *ProxyPool) Binding(accountID string) string {
	if p == nil {
		return ""
	}
	p.pool.mu.Lock()
	account := p.pool.byID[strings.TrimSpace(accountID)]
	p.pool.mu.Unlock()
	if account == nil || strings.TrimSpace(account.Config.Proxy) != "" {
		return ""
	}
	if bound := account.boundProxy.Load(); bound != nil {
		return *bound
	}
	return ""
}

// Available checks whether proxy is among currently participating proxies.
func (p *ProxyPool) Available(proxy string) bool {
	if p == nil || proxy == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.availableLocked(), proxy)
}

// entryLocked returns the entry corresponding to the proxy, or nil if not in the pool.
func (p *ProxyPool) entryLocked(proxy string) *proxyEntry {
	for _, entry := range p.entries {
		if entry.proxy == proxy {
			return entry
		}
	}
	return nil
}

// ReportFailure records network errors for requests through proxy; marks proxy unavailable and rebinds when threshold is reached and other available proxies exist, returning accounts with changed bindings.
func (p *ProxyPool) ReportFailure(proxy string, err error) ([]string, error) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entryLocked(proxy)
	if entry == nil {
		return nil, nil
	}
	entry.failures++
	if err != nil {
		entry.lastError = err.Error()
	}
	if entry.down || entry.failures < proxyFailureThreshold || !p.otherAvailableLocked(entry) {
		return nil, nil
	}
	entry.down = true
	return p.assignLocked()
}

// ReportSuccess clears consecutive network errors for proxy.
func (p *ProxyPool) ReportSuccess(proxy string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry := p.entryLocked(proxy); entry != nil {
		entry.failures = 0
	}
}

func (p *ProxyPool) otherAvailableLocked(target *proxyEntry) bool {
	for _, entry := range p.entries {
		if entry != target && !entry.down {
			return true
		}
	}
	return false
}

// Probe tests proxies using check: marks failing proxies as unavailable, restores recovered proxies to rotation, rebinds accounts from unavailable proxies to available ones, and returns accounts with changed bindings.
func (p *ProxyPool) Probe(ctx context.Context, check func(context.Context, string) error) ([]string, error) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	proxies := make([]string, 0, len(p.entries))
	for _, entry := range p.entries {
		proxies = append(proxies, entry.proxy)
	}
	p.mu.Unlock()
	results := make([]error, len(proxies))
	var wait sync.WaitGroup
	for index, proxy := range proxies {
		wait.Go(func() {
			results[index] = check(ctx, proxy)
		})
	}
	wait.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC()
	for index, proxy := range proxies {
		for _, entry := range p.entries {
			if entry.proxy != proxy {
				continue
			}
			entry.checkedAt = now
			if results[index] != nil {
				entry.down = true
				entry.lastError = results[index].Error()
				continue
			}
			entry.down = false
			entry.failures = 0
			entry.lastError = ""
		}
	}
	return p.assignLocked()
}

// Status returns availability and bound account counts for each proxy.
func (p *ProxyPool) Status() []ProxyStatus {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	counts := make(map[string]int, len(p.entries))
	p.pool.mu.Lock()
	for _, account := range p.pool.accounts {
		if strings.TrimSpace(account.Config.Proxy) != "" {
			continue
		}
		if bound := account.boundProxy.Load(); bound != nil {
			counts[*bound]++
		}
	}
	p.pool.mu.Unlock()
	statuses := make([]ProxyStatus, 0, len(p.entries))
	for _, entry := range p.entries {
		status := ProxyStatus{Proxy: entry.proxy, Available: !entry.down, Accounts: counts[entry.proxy], Error: entry.lastError}
		if !entry.checkedAt.IsZero() {
			status.CheckedAt = timePointer(entry.checkedAt)
		}
		statuses = append(statuses, status)
	}
	return statuses
}
