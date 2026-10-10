package aistudio

import (
	"reflect"
	"testing"
	"time"
)

// TestBrowserRoundTripperFirefoxHTTP2 verifies browser transport uses Firefox 152 HTTP/2 frame parameters and PING keepalive
func TestBrowserRoundTripperFirefoxHTTP2(t *testing.T) {
	roundTripper, err := newBrowserRoundTripper("")
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripper.(*browserRoundTripper).transport
	if transport.ReadIdleTimeout != 58*time.Second || transport.PingTimeout != 8*time.Second {
		t.Fatalf("PING threshold=%s timeout=%s, expected Firefox defaults 58s and 8s", transport.ReadIdleTimeout, transport.PingTimeout)
	}
	profile := firefox152Profile()
	if !reflect.DeepEqual(transport.Settings, profile.GetSettings()) || !reflect.DeepEqual(transport.SettingsOrder, profile.GetSettingsOrder()) {
		t.Fatalf("SETTINGS=%v order=%v, expected Firefox profile %v order %v", transport.Settings, transport.SettingsOrder, profile.GetSettings(), profile.GetSettingsOrder())
	}
	if transport.ConnectionFlow != profile.GetConnectionFlow() {
		t.Fatalf("connection flow window=%d, expected %d", transport.ConnectionFlow, profile.GetConnectionFlow())
	}
	if !reflect.DeepEqual(transport.PseudoHeaderOrder, profile.GetPseudoHeaderOrder()) || !reflect.DeepEqual(transport.HeaderPriority, profile.GetHeaderPriority()) {
		t.Fatalf("pseudo header order=%v header priority=%+v does not match Firefox profile", transport.PseudoHeaderOrder, transport.HeaderPriority)
	}
}
