package task

// InWindow reports whether tick falls inside [from, to).
func InWindow(tick, from, to int) bool {
	return tick >= from && tick < to
}
