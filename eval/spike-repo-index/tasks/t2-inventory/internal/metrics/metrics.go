// Package metrics keeps process-wide counters for the service's own reports.
package metrics

import "sync"

// Registry holds named counters. It is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	counters map[string]int64
}

// New returns an empty registry.
func New() *Registry { return &Registry{counters: map[string]int64{}} }

// Inc adds one to the counter named key.
func (r *Registry) Inc(key string) { r.Add(key, 1) }

// Add adds n to the counter named key.
func (r *Registry) Add(key string, n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[key] += n
}

// Get reports the counter's value, zero when it was never incremented.
func (r *Registry) Get(key string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters[key]
}

// Snapshot copies the counters, which is what a report should read: the live
// map must not change under a writer.
func (r *Registry) Snapshot() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int64, len(r.counters))
	for k, v := range r.counters {
		out[k] = v
	}
	return out
}
