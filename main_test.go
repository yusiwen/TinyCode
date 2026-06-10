package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectContextNoFile(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	ctx := loadProjectContext()
	if ctx != "" {
		t.Errorf("expected empty for no file, got %q", ctx)
	}
}

func TestLoadProjectContextAGENTSMD(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	content := "This project uses Go 1.24 and follows standard Go conventions."
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644)

	ctx := loadProjectContext()
	if ctx != content {
		t.Errorf("expected AGENTS.md content, got %q", ctx)
	}
}

func TestLoadProjectContextCLAUDEMD(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	content := "Always run tests before committing."
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644)

	ctx := loadProjectContext()
	if ctx != content {
		t.Errorf("expected CLAUDE.md content, got %q", ctx)
	}
}

func TestLoadProjectContextPrecedence(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	// Both files exist — AGENTS.md takes precedence
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents content"), 0644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude content"), 0644)

	ctx := loadProjectContext()
	if ctx != "agents content" {
		t.Errorf("expected AGENTS.md content (first match), got %q", ctx)
	}
}
