package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeUserConfig seeds ~/.tinycode/config.json under a redirected HOME and
// returns the file path.
func writeUserConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAllowedPathGrantCarriesContext covers what a persistent grant has to say
// about itself: which path, when, and from which project — the context a person
// needs to decide whether to revoke it.
func TestAllowedPathGrantCarriesContext(t *testing.T) {
	path := writeUserConfig(t, `{"sandbox":{"allowed_paths":["/legacy/one"]}}`)

	if err := AddAllowedPathGrant("/granted/two", "/home/me/project"); err != nil {
		t.Fatalf("AddAllowedPathGrant: %v", err)
	}
	// Adding it again must not duplicate it or rewrite its history: the record
	// describes when the grant was actually made.
	if err := AddAllowedPathGrant("/granted/two", "/somewhere/else"); err != nil {
		t.Fatalf("AddAllowedPathGrant (repeat): %v", err)
	}
	if err := AddAllowedPathGrant("", ""); err == nil {
		t.Error("AddAllowedPathGrant accepted an empty path")
	}

	grants, err := ListAllowedPathGrants()
	if err != nil {
		t.Fatalf("ListAllowedPathGrants: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %+v, want the legacy entry and the new one", grants)
	}

	byPath := map[string]AllowedPathGrant{}
	for _, g := range grants {
		byPath[g.Path] = g
	}
	if g := byPath["/legacy/one"]; !g.Legacy {
		t.Errorf("legacy grant = %+v, want it marked as recorded before grants carried context", g)
	}
	got := byPath["/granted/two"]
	if got.Project != "/home/me/project" {
		t.Errorf("project = %q, want the project the grant was made from", got.Project)
	}
	if got.Granted.IsZero() || time.Since(got.Granted) > time.Hour {
		t.Errorf("granted = %v, want a timestamp from this run", got.Granted)
	}

	// The file stays a config file: unknown keys survive and the legacy list is
	// not rewritten by an addition.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/legacy/one") {
		t.Error("the legacy list was dropped by an addition")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("the config is no longer valid JSON: %v", err)
	}
}

// TestRevokeAllowedPathGrantReachesBothForms pins the property that makes
// revocation usable: every way a grant can be stored, it can be removed.
func TestRevokeAllowedPathGrantReachesBothForms(t *testing.T) {
	writeUserConfig(t, `{"sandbox":{"allowed_paths":["/legacy/one","/legacy/keep"]}}`)

	if err := AddAllowedPathGrant("/recorded/two", "/project"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/legacy/one", "/recorded/two"} {
		removed, err := RevokeAllowedPathGrant(path)
		if err != nil {
			t.Fatalf("RevokeAllowedPathGrant(%q): %v", path, err)
		}
		if !removed {
			t.Fatalf("RevokeAllowedPathGrant(%q) reported nothing removed", path)
		}
	}

	removed, err := RevokeAllowedPathGrant("/never/existed")
	if err != nil {
		t.Fatalf("RevokeAllowedPathGrant: %v", err)
	}
	if removed {
		t.Error("revoking a path that was never granted reported a removal")
	}

	grants, err := ListAllowedPathGrants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Path != "/legacy/keep" {
		t.Fatalf("grants = %+v, want only the entry that was not revoked", grants)
	}
}

// TestGrantRecordSupersedesTheLegacyEntry covers the migration path: a path in
// both forms is listed once, described by its record.
func TestGrantRecordSupersedesTheLegacyEntry(t *testing.T) {
	writeUserConfig(t, `{"sandbox":{"allowed_paths":["/both"],"allowed_path_grants":[{"path":"/both","project":"/p","granted":"2026-01-02T03:04:05Z"}]}}`)

	grants, err := ListAllowedPathGrants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("grants = %+v, want one entry for a path stored twice", grants)
	}
	if grants[0].Legacy || grants[0].Project != "/p" {
		t.Fatalf("grant = %+v, want the record to describe the path", grants[0])
	}
}

// TestListGrantsOnMissingConfigIsEmpty covers the first run: nothing configured
// is not an error, and it must not create a file just by being asked.
func TestListGrantsOnMissingConfigIsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	grants, err := ListAllowedPathGrants()
	if err != nil {
		t.Fatalf("ListAllowedPathGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants = %+v, want none", grants)
	}
	if _, err := os.Stat(filepath.Join(home, ".tinycode", "config.json")); err == nil {
		t.Error("listing grants created a config file")
	}
}
