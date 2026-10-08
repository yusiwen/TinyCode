//go:build darwin

package tool

// defaultProbeContainment reports the mechanism the open layer uses on macOS.
//
// There is no openat2 here, so every open goes through the component walk: each
// component is opened with O_NOFOLLOW, and a component that is a symlink — which
// a resolved path cannot legitimately contain, and which is therefore one
// swapped in after the check — is refused with ELOOP rather than followed. That
// is a kernel decision attached to the open, so the level is kernel; what macOS
// lacks is the single-syscall form, not the boundary.
func defaultProbeContainment() Containment {
	return Containment{Level: ContainmentKernel, Mechanism: mechanismOpenatWalk}
}
