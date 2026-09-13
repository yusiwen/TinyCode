//go:build linux

package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// TestKernelEscapeCheck exercises the openat2(RESOLVE_BENEATH) layer that the
// sandbox adds on Linux. CI (ubuntu-latest) runs this; on kernels without
// openat2 (< 5.6) the layer is inert and the test skips.
func TestKernelEscapeCheck(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "up")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Warm up so an unsupported kernel is detected before the assertions.
	kernelEscapeCheck(root, filepath.Join(root, "warmup"))
	if openat2Unsupported.Load() {
		t.Skip("kernel does not support openat2 (Linux < 5.6); the layer is inert")
	}

	cases := []struct {
		name   string
		path   string
		escape bool
	}{
		{"root itself", root, false},
		{"in-root existing file", filepath.Join(root, "inside.txt"), false},
		{"in-root path not created yet", filepath.Join(root, "sub", "new.txt"), false},
		{"symlink pointing outside", filepath.Join(root, "up", "secret.txt"), true},
		{"symlink plus dotdot", root + "/up/../outside/secret.txt", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kernelEscapeCheck(root, tc.path); got != tc.escape {
				t.Errorf("kernelEscapeCheck(%q) = %v, want %v", tc.path, got, tc.escape)
			}
			// The sandbox must deny the escaping paths end to end.
			sandbox := &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}
			err := sandbox.CheckPath(tc.path)
			if tc.escape && err == nil {
				t.Errorf("CheckPath(%q) allowed an escaping path", tc.path)
			}
			if !tc.escape && err != nil {
				t.Errorf("CheckPath(%q) = %v, want allowed", tc.path, err)
			}
		})
	}
}
