package tool

import "strings"

// relBeneath returns path's remainder relative to root, plus whether path is
// lexically beneath root at all.
//
// The remainder keeps ".." components verbatim. Cleaning them away would
// collapse "link/.." lexically and hide the very escape the caller asks about:
// the OS applies ".." to the already-resolved link target, so
// "root/up/../outside" reads outside the root when "up" is a symlink, while
// "root/outside" is what a lexical collapse would compare. Redundant
// separators and "." components are dropped because they carry no resolution
// semantics.
//
// Paths are split on "/" (openat2's separator); the only caller is the Linux
// kernel-containment layer. The helper itself is portable so its rules can be
// tested on every platform.
func relBeneath(root, path string) (string, bool) {
	rootParts := pathComponents(root)
	pathParts := pathComponents(path)

	// An empty root is "/", which nothing can escape, and a path that is not
	// strictly longer than the root has no remainder to hand to the kernel.
	if len(rootParts) == 0 || len(pathParts) <= len(rootParts) {
		return "", false
	}
	for i, part := range rootParts {
		if pathParts[i] != part {
			return "", false
		}
	}
	return strings.Join(pathParts[len(rootParts):], "/"), true
}

// pathComponents splits a path into its non-empty, non-"." components, keeping
// ".." so callers can preserve OS resolution order.
func pathComponents(path string) []string {
	var parts []string
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." {
			continue
		}
		parts = append(parts, seg)
	}
	return parts
}
