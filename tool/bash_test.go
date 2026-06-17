package tool

import (
	"testing"
)

func TestCheckPlanModeWriteMkdir(t *testing.T) {
	err := checkPlanModeWrite("mkdir test")
	if err == nil {
		t.Fatal("expected error for mkdir")
	}
}

func TestCheckPlanModeWriteHeredoc(t *testing.T) {
	err := checkPlanModeWrite("cat > file.txt << 'EOF'\nhello\nEOF")
	if err == nil {
		t.Fatal("expected error for heredoc")
	}
}

func TestCheckPlanModeWriteRedirect(t *testing.T) {
	err := checkPlanModeWrite("echo hello > file.txt")
	if err == nil {
		t.Fatal("expected error for redirect")
	}
}

func TestCheckPlanModeWriteAppend(t *testing.T) {
	err := checkPlanModeWrite("echo hello >> file.txt")
	if err == nil {
		t.Fatal("expected error for append redirect")
	}
}

func TestCheckPlanModeReadOnly(t *testing.T) {
	err := checkPlanModeWrite("ls -la")
	if err != nil {
		t.Fatalf("expected no error for ls, got: %v", err)
	}
}

func TestCheckPlanModeReadCat(t *testing.T) {
	err := checkPlanModeWrite("cat file.txt")
	if err != nil {
		t.Fatalf("expected no error for cat, got: %v", err)
	}
}

func TestCheckPlanModeReadGrep(t *testing.T) {
	err := checkPlanModeWrite("grep 'foo' *.go")
	if err != nil {
		t.Fatalf("expected no error for grep, got: %v", err)
	}
}

func TestCheckPlanModeRedirectDevNull(t *testing.T) {
	err := checkPlanModeWrite("grep foo file.txt > /dev/null")
	if err != nil {
		t.Fatalf("expected no error for redirect to /dev/null, got: %v", err)
	}
}

func TestCheckPlanModeRedirectStderr(t *testing.T) {
	err := checkPlanModeWrite("grep foo file.txt 2>/dev/null")
	if err != nil {
		t.Fatalf("expected no error for stderr redirect, got: %v", err)
	}
}

func TestCheckPlanModeRedirectBoth(t *testing.T) {
	err := checkPlanModeWrite("grep foo file.txt &>/dev/null")
	if err != nil {
		t.Fatalf("expected no error for combined redirect to null, got: %v", err)
	}
}

func TestCheckPlanModeWriteRm(t *testing.T) {
	err := checkPlanModeWrite("rm -rf /tmp/test")
	if err == nil {
		t.Fatal("expected error for rm")
	}
}

func TestCheckPlanModeWriteCp(t *testing.T) {
	err := checkPlanModeWrite("cp a.txt b.txt")
	if err == nil {
		t.Fatal("expected error for cp")
	}
}

func TestCheckPlanModeWriteTouch(t *testing.T) {
	err := checkPlanModeWrite("touch newfile.txt")
	if err == nil {
		t.Fatal("expected error for touch")
	}
}

func TestCheckPlanModeWriteMv(t *testing.T) {
	err := checkPlanModeWrite("mv a.txt b.txt")
	if err == nil {
		t.Fatal("expected error for mv")
	}
}

func TestCheckPlanModeWriteChained(t *testing.T) {
	err := checkPlanModeWrite("ls && mkdir test")
	if err == nil {
		t.Fatal("expected error for mkdir in chained command")
	}
}

func TestCheckPlanModeWriteSemicolon(t *testing.T) {
	err := checkPlanModeWrite("ls; rm file.txt")
	if err == nil {
		t.Fatal("expected error for rm in semicolon command")
	}
}

func TestCheckPlanModeWritePipe(t *testing.T) {
	// Pipelines should be allowed
	err := checkPlanModeWrite("cat file.txt | grep foo")
	if err != nil {
		t.Fatalf("expected no error for pipe, got: %v", err)
	}
}
