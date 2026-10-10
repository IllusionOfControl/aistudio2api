package aistudio

import (
	"os"
	"runtime"
	"testing"
)

// TestUserCacheRootWithoutEnvironment verifies resolving the same user cache directory when environment variables are missing
func TestUserCacheRootWithoutEnvironment(t *testing.T) {
	expected, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("environment has no user cache directory: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", "")
	} else {
		if os.Getenv("XDG_CACHE_HOME") != "" {
			t.Skip("XDG_CACHE_HOME specifies a non-default cache directory")
		}
		t.Setenv("HOME", "")
	}
	actual, err := userCacheRoot()
	if err != nil {
		t.Fatalf("userCacheRoot: %v", err)
	}
	if actual != expected {
		t.Fatalf("userCacheRoot = %q, want %q", actual, expected)
	}
}
