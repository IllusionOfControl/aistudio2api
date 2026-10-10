package setup

import (
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

// TestSetupRemoteOrigin verifies remote address only accepts HTTPS origins and local HTTP
func TestSetupRemoteOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://example.com":       "https://example.com",
		"https://example.com:8443/": "https://example.com:8443",
		"http://127.0.0.1:2048":     "http://127.0.0.1:2048",
		"http://localhost:2048":     "http://localhost:2048",
		"http://[::1]:2048":         "http://[::1]:2048",
		"http://192.168.1.2:2048":   "",
		"http://example.com":        "",
		"https://example.com/api":   "",
		"https://u:p@example.com":   "",
		"https://example.com?a=1":   "",
		"example.com":               "",
	} {
		got, err := remoteOrigin(raw)
		if want == "" && err == nil || want != "" && (err != nil || got != want) {
			t.Errorf("remoteOrigin(%q) = %q, %v", raw, got, err)
		}
	}
	if _, err := parseSetupFlags([]string{"--token", "abc"}, config.Config{}); err == nil {
		t.Fatal("accepted --token without --remote")
	}
}
