package app

import (
	"net/url"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// systemProxyTarget is the target URL used to check whether system proxy applies to upstream.
var systemProxyTarget = &url.URL{Scheme: "https", Host: "aistudio.google.com"}

// systemProxy returns the proxy URL from environment variables or OS settings that applies to upstream, or empty if unset.
func systemProxy() string {
	if proxy, err := httpproxy.FromEnvironment().ProxyFunc()(systemProxyTarget); err == nil && proxy != nil {
		return proxy.String()
	}
	return windowsProxyURL(osProxyServer())
}

// windowsProxyURL converts Windows ProxyServer host:port or protocol-specific settings to a proxy URL, checking https, http, and socks in order.
func windowsProxyURL(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if !strings.Contains(server, "=") {
		if strings.Contains(server, "://") {
			return server
		}
		return "http://" + server
	}
	entries := make(map[string]string)
	for _, item := range strings.Split(server, ";") {
		if name, address, ok := strings.Cut(strings.TrimSpace(item), "="); ok {
			entries[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(address)
		}
	}
	for _, name := range []string{"https", "http"} {
		if address := entries[name]; address != "" {
			return "http://" + address
		}
	}
	if address := entries["socks"]; address != "" {
		return "socks5://" + address
	}
	return ""
}

// redactedProxy returns the proxy URL with password masked.
func redactedProxy(proxy string) string {
	parsed, err := url.Parse(proxy)
	if err != nil {
		return proxy
	}
	return parsed.Redacted()
}
