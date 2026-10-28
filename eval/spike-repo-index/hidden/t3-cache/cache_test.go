package task

import "testing"

func TestCacheExpiresAtTheBoundary(t *testing.T) {
	clock := &Clock{}
	cache := NewCache(clock, 8, 3)
	cache.Put("k", 42)
	if got := cache.Get("k"); got != 42 {
		t.Fatalf("immediately after Put, Get = %d, want 42", got)
	}
	clock.Tick()
	clock.Tick()
	if got := cache.Get("k"); got != 42 {
		t.Fatalf("two ticks after the write, Get = %d, want 42", got)
	}
	clock.Tick()
	if got := cache.Get("k"); got != 0 {
		t.Fatalf("exactly ttl ticks after the write, Get = %d, want 0", got)
	}
}

func TestCacheWriteRefreshesTheDeadline(t *testing.T) {
	clock := &Clock{}
	cache := NewCache(clock, 8, 3)
	cache.Put("k", 1)
	clock.Tick()
	clock.Tick()
	cache.Put("k", 2)
	clock.Tick()
	clock.Tick()
	if got := cache.Get("k"); got != 2 {
		t.Fatalf("Get = %d, want 2: the second write refreshed the deadline", got)
	}
	clock.Tick()
	if got := cache.Get("k"); got != 0 {
		t.Fatalf("Get = %d, want 0 one ttl after the refresh", got)
	}
}

func TestZeroCapacityStoresNothing(t *testing.T) {
	clock := &Clock{}
	cache := NewCache(clock, 0, 3)
	cache.Put("k", 7)
	if got := cache.Get("k"); got != 0 {
		t.Fatalf("Get = %d, want 0 when the capacity is zero", got)
	}
}

func TestZeroTTLNeverExpires(t *testing.T) {
	clock := &Clock{}
	cache := NewCache(clock, 8, 0)
	cache.Put("k", 9)
	for i := 0; i < 100; i++ {
		clock.Tick()
	}
	if got := cache.Get("k"); got != 9 {
		t.Fatalf("Get = %d, want 9 when ttl is zero", got)
	}
}
