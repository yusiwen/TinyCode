package tool

import "testing"

// TestRelBeneath pins the portable half of the Linux kernel-containment layer:
// how a path is turned into the remainder the kernel is asked to resolve.
func TestRelBeneath(t *testing.T) {
	cases := []struct {
		name   string
		root   string
		path   string
		want   string
		wantOK bool
	}{
		{"in-root file", "/a/root", "/a/root/inside.txt", "inside.txt", true},
		{"in-root nested", "/a/root", "/a/root/sub/new.txt", "sub/new.txt", true},
		{"root itself", "/a/root", "/a/root", "", false},
		{"outside", "/a/root", "/a/other/x", "", false},
		{"sibling with a shared prefix", "/a/root", "/a/rootfoo/x", "", false},
		{"parent", "/a/root", "/a", "", false},

		// ".." must survive: the OS applies it to the resolved link target, so
		// collapsing it here would hide the "link/.." escape.
		{"link plus dotdot", "/a/root", "/a/root/up/../outside/secret.txt", "up/../outside/secret.txt", true},
		{"dotdot out of root", "/a/root", "/a/root/../other/x", "../other/x", true},
		{"in-root dotdot", "/a/root", "/a/root/a/../b/file.txt", "a/../b/file.txt", true},

		// Redundant separators and "." carry no resolution semantics.
		{"double separator", "/a/root", "/a/root//x", "x", true},
		{"dot component", "/a/root", "/a/root/./x", "x", true},
		{"trailing separator", "/a/root", "/a/root/x/", "x", true},
		{"messy root", "/a//root/", "/a/root/x", "x", true},

		// A root of "/" is the whole filesystem: nothing to probe against.
		{"slash root", "/", "/etc/passwd", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := relBeneath(tc.root, tc.path)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("relBeneath(%q, %q) = (%q, %v), want (%q, %v)",
					tc.root, tc.path, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestPathComponents keeps the splitter honest about ".." and ".".
func TestPathComponents(t *testing.T) {
	got := pathComponents("/a//b/./c/../d/")
	want := []string{"a", "b", "c", "..", "d"}
	if len(got) != len(want) {
		t.Fatalf("pathComponents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pathComponents = %v, want %v", got, want)
		}
	}
}
