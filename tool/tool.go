package tool

import "context"

// Tool is a single action the agent can invoke.
// Mirrors agent.Tool but lives in its own package for clean dependency management.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
	Execute     func(ctx context.Context, args map[string]any) (string, error)
}

// Registry holds all available tools.
type Registry struct {
	tools []Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools = append(r.tools, t)
}

// List returns all registered tools.
func (r *Registry) List() []Tool {
	cp := make([]Tool, len(r.tools))
	copy(cp, r.tools)
	return cp
}

// Find looks up a tool by name.
func (r *Registry) Find(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Name == name {
			return t, true
			}
	}
	return Tool{}, false
}
