package task

// Clock counts ticks. Every call to Tick advances it by one.
type Clock struct {
	now int
}

// Tick advances the clock and returns the new instant.
func (c *Clock) Tick() int {
	c.now++
	return c.now
}

// Now reports the current instant.
func (c *Clock) Now() int { return c.now }
