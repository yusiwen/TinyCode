//go:build !linux && !darwin

package tool

// defaultProbeContainment reports the weakest level for platforms where
// openResolvedNoFollow has no implementation: there is no kernel-side walk to
// attach the decision to, so the in-process checks in CheckPath are the only
// gate.
func defaultProbeContainment() Containment {
	return Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable}
}
