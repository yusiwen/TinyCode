// Package testenv holds the platform facts a test has to ask about instead of
// assuming.
//
// It exists because the suite grew on Unix and the Windows job (roadmap C8) is
// where those assumptions show themselves. Each helper skips the calling test
// with a reason, so a Windows run reports "not applicable here" rather than a
// failure that says nothing about the code.
package testenv

import (
	"runtime"
	"testing"
)

// POSIXModes skips a test that asserts Unix permission bits. Windows has no such
// bits: every file reports 0666 and chmod is a no-op, while the code that sets
// 0600 is still correct — it is the assertion that cannot mean anything there.
func POSIXModes(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not modelled on Windows; the mode the code sets is correct but unobservable there")
	}
}

// Symlinks skips a test that needs to create a symbolic link. Windows lets only a
// privileged process (or Developer Mode) create one, so a normal user gets a
// permission error from os.Symlink rather than a link.
func Symlinks(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege Windows does not grant a normal process")
	}
}

// DesktopNotifier skips a test that asserts a desktop notification was emitted.
// Windows has no notifier in this project (docs/windows.md), so the probe finds
// no command and the test would assert the absence as a defect.
func DesktopNotifier(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no desktop notifier for Windows in this project; the run finishes without one")
	}
}
