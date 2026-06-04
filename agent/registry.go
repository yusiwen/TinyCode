package agent

import (
	"fmt"
)

// Registry holds named agent configurations and tracks the active agent.
type Registry struct {
	agents  map[string]*AgentConfig
	current string // name of the currently active agent
	order   []string // insertion order for cycling
	pos     int
}

// NewRegistry creates a registry and registers the default agents.
// The first registered agent becomes the current one.
func NewRegistry() *Registry {
	r := &Registry{
		agents: make(map[string]*AgentConfig),
		order:  make([]string, 0),
		pos:    0,
	}
	for _, cfg := range DefaultAgents() {
		r.Register(*cfg)
	}
	// Default to plan mode
	r.current = "plan"
	r.pos = 0
	return r
}

// Register adds or replaces an agent config.
func (r *Registry) Register(cfg AgentConfig) {
	r.agents[cfg.Name] = &cfg
	// Add to order list if not already present
	for _, name := range r.order {
		if name == cfg.Name {
			return
		}
	}
	r.order = append(r.order, cfg.Name)
}

// Get returns the config for a named agent.
func (r *Registry) Get(name string) (*AgentConfig, error) {
	cfg, ok := r.agents[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent: %s", name)
	}
	return cfg, nil
}

// Current returns the currently active agent's config.
func (r *Registry) Current() *AgentConfig {
	return r.agents[r.current]
}

// CurrentName returns the name of the currently active agent.
func (r *Registry) CurrentName() string {
	return r.current
}

// Switch cycles to the next primary agent in order.
// Returns the new agent name.
func (r *Registry) Switch() string {
	// Find next primary agent in order (skip subagents)
	attempts := 0
	for attempts < len(r.order) {
		r.pos = (r.pos + 1) % len(r.order)
		name := r.order[r.pos]
		cfg := r.agents[name]
		if cfg != nil && cfg.Mode == AgentModePrimary {
			r.current = name
			return name
		}
		attempts++
	}
	return r.current
}

// Set switches to a specific agent by name.
func (r *Registry) Set(name string) error {
	cfg, ok := r.agents[name]
	if !ok {
		return fmt.Errorf("unknown agent: %s", name)
	}
	if cfg.Mode != AgentModePrimary {
		return fmt.Errorf("agent %s is a subagent, cannot be a session agent", name)
	}
	r.current = name
	// Update position to match
	for i, n := range r.order {
		if n == name {
			r.pos = i
			break
		}
	}
	return nil
}

// List returns all registered agent configs.
func (r *Registry) List() []*AgentConfig {
	result := make([]*AgentConfig, 0, len(r.agents))
	for _, name := range r.order {
		if cfg, ok := r.agents[name]; ok {
			result = append(result, cfg)
		}
	}
	return result
}

// ToolAllowed checks if a given tool is permitted for the active agent.
func (r *Registry) ToolAllowed(toolName string) bool {
	cfg := r.Current()
	return ToolAllowedFor(cfg, toolName)
}

// ToolAllowedFor checks if a given tool is permitted for a specific agent config.
func ToolAllowedFor(cfg *AgentConfig, toolName string) bool {
	// Check denied list first
	for _, d := range cfg.DeniedTools {
		if d == toolName {
			return false
		}
	}
	// Check allowed list
	for _, a := range cfg.AllowedTools {
		if a == "*" {
			return true
		}
		if a == toolName {
			return true
		}
	}
	// Not in allowed list and not "*" → denied
	return false
}
