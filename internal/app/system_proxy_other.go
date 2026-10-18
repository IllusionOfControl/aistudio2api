//go:build !windows

package app

// osProxyServer returns empty on non-Windows systems where environment variables supply the system proxy.
func osProxyServer() string {
	return ""
}
