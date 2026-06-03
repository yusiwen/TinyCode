package skill

import "context"

// Step is a single step in a skill's execution plan.
type Step struct {
	ToolName string         `json:"tool_name"`
	Input    map[string]any `json:"input"`
}

// Skill is a multi-step, higher-level capability composed of multiple tools.
// Skills are registered at startup and exposed to the LLM as tools.
type Skill struct {
	Name        string
	Description string
	Steps       []Step           // ordered steps executed in sequence
	Handler     func(ctx context.Context, args map[string]any) (string, error)
}

// Registry manages all registered skills.
type Registry struct {
	skills []Skill
}

// NewRegistry creates an empty skill registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a skill.
func (r *Registry) Register(s Skill) {
	r.skills = append(r.skills, s)
}

// List returns all registered skills.
func (r *Registry) List() []Skill {
	cp := make([]Skill, len(r.skills))
	copy(cp, r.skills)
	return cp
}
