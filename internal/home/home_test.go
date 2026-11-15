package home

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDirPrefersHOME is the rule the whole tree depends on: a redirected HOME is
// the answer, on every platform. On Windows the platform's own function would
// return USERPROFILE and every file under the redirected home would be invisible.
func TestDirPrefersHOME(t *testing.T) {
	want := filepath.Join(t.TempDir(), "redirected-home")
	t.Setenv("HOME", want)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != want {
		t.Errorf("Dir() = %q, want the redirected HOME %q", got, want)
	}
}

// TestDirFallsBackToThePlatform covers the case where HOME is unset: the platform
// still knows, and an error there is the caller's to handle.
func TestDirFallsBackToThePlatform(t *testing.T) {
	t.Setenv("HOME", "")
	_ = os.Unsetenv("HOME")
	got, err := Dir()
	if err != nil {
		t.Skipf("this host cannot answer where home is: %v", err)
	}
	if got == "" {
		t.Error("Dir() returned an empty home without an error")
	}
}
