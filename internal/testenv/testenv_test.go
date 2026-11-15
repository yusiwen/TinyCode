package testenv

import "testing"

// TestTheHelpersDoNotSkipThePlatformTheyAreRunningOn keeps the helpers honest on
// Unix: a helper that always skipped would silently disable the assertions it was
// added to.
func TestTheHelpersDoNotSkipThePlatformTheyAreRunningOn(t *testing.T) {
	// On Unix none of the three may skip, so reaching the end proves it. On
	// Windows they are expected to skip, and this test skips with them.
	POSIXModes(t)
	Symlinks(t)
	DesktopNotifier(t)
}
