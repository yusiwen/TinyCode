package agent

// AgentMode indicates how an agent is used.
type AgentMode string

const (
	AgentModePrimary  AgentMode = "primary"  // Can be a session default agent
	AgentModeSubagent AgentMode = "subagent" // Only invoked via task tool
)

// AgentConfig defines the configuration for a named agent.
type AgentConfig struct {
	Name         string
	Mode         AgentMode
	Description  string
	SystemPrompt string
	MaxSteps     int       // 0 = unlimited
	AllowedTools []string  // tool names allowed ("*" = all)
	DeniedTools  []string  // tool names explicitly denied
}

// DefaultAgents returns the built-in agent configurations.
func DefaultAgents() map[string]*AgentConfig {
	return map[string]*AgentConfig{
		"plan": {
			Name:        "plan",
			Mode:        AgentModePrimary,
			Description: "Plan mode — read-only analysis. Explore the codebase, search for patterns, read files, but CANNOT modify anything.",
			SystemPrompt: "You are TinyCode in PLAN mode. You can explore and analyze the codebase " +
				"but you CANNOT modify any files. You cannot use write_file, git_commit, " +
				"sandbox_allow, or task.\n\n" +
				"Strategy for efficient analysis:\n" +
				"1. First explore the project structure (bash: tree/find/ls)\n" +
				"2. Read key project files (go.mod, main.go, Makefile, config files)\n" +
				"3. Read core package files (the main logic)\n" +
				"4. Only read detail files if needed (implementation details, tests)\n\n" +
				"Use multiple tool calls in a single response to read several files " +
				"at once (e.g. read_file + read_file in one response).\n" +
				"When you have finished analyzing, tell the user what you found.",
			MaxSteps: 20,
			AllowedTools: []string{"*"},
			DeniedTools: []string{"write_file", "git_commit", "sandbox_allow", "task"},
		},
		"build": {
			Name:        "build",
			Mode:        AgentModePrimary,
			Description: "Build mode — full access. Read, write, edit files, run commands, delegate to sub-agents.",
			SystemPrompt: "You are TinyCode in BUILD mode. You have full access to all tools " +
				"including write_file, git_commit, and the task tool for delegating to " +
				"sub-agents. Use tools to implement changes and verify your work.",
			MaxSteps:    30,
			AllowedTools: []string{"*"},
			DeniedTools:  []string{},
		},
		"explore": {
			Name:        "explore",
			Mode:        AgentModeSubagent,
			Description: "Fast code exploration sub-agent. Searches files, reads code, runs commands. No edit permissions.",
			SystemPrompt: "You are a focused explore sub-agent. Your job is to find information " +
				"in the codebase. You can use bash, read_file, and search_files. " +
				"Return a concise summary of what you found. Do NOT modify any files.",
			MaxSteps: 15,
			AllowedTools: []string{"bash", "read_file", "search_files"},
			DeniedTools:  []string{},
		},
	}
}
