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
	Hidden       bool     // hidden from Tab switching and user-facing lists
	Description  string
	SystemPrompt string
	MaxSteps     int       // 0 = unlimited
	AllowedTools []string  // tool names allowed ("*" = all)
	DeniedTools  []string  // tool names explicitly denied
	Permissions  Ruleset   // permission rules (replaces DeniedTools, Phase 3)
}

// IsToolAllowed returns true if the named tool is permitted by this config.
// Checks Permissions first (Phase 3), then falls back to DeniedTools/AllowedTools.
func (c *AgentConfig) IsToolAllowed(name string) bool {
	if len(c.Permissions) > 0 {
		return Evaluate(name, "*", c.Permissions...) != EffectDeny
	}
	for _, d := range c.DeniedTools {
		if d == name {
			return false
		}
	}
	if c.AllowedTools != nil {
		for _, a := range c.AllowedTools {
			if a == name {
				return true
			}
		}
		return false
	}
	return true
}

// DefaultAgents returns the built-in agent configurations.
func DefaultAgents() map[string]*AgentConfig {
	return map[string]*AgentConfig{
		"plan": {
			Name:        "plan",
			Mode:        AgentModePrimary,
			Description: "Plan mode — read-only analysis. Explore the codebase, search for patterns, read files, but CANNOT modify anything.",
			SystemPrompt: "You are TinyCode in PLAN mode. You can only read and analyze. " +
				"All write operations (bash >, mkdir, rm, cat, etc.) are BLOCKED by the system. " +
				"Write operations that attempt to create or modify files will be rejected with [PLAN MODE BLOCKED].\n\n" +
				"Your task is to:\n" +
				"1. Explore the codebase using read-only commands (ls, find, grep, cat without redirect)\n" +
				"2. Understand the user's request\n" +
				"3. Create a clear implementation plan\n" +
				"4. Present the plan to the user\n" +
				"5. Ask the user if they want to proceed — tell them to switch to build mode\n\n" +
				"Strategy for efficient analysis:\n" +
				"1. First explore the project structure (bash: tree/find/ls)\n" +
				"2. Read key project files (go.mod, main.go, Makefile, config files)\n" +
				"3. Read core package files (the main logic)\n" +
				"4. Only read detail files if needed (implementation details, tests)\n\n" +
				"IMPORTANT: You CANNOT write files or execute write commands in plan mode. " +
				"After analyzing, create a detailed plan for the user and ask them to type /build to execute it.",
			MaxSteps: 20,
			Permissions: Ruleset{
				{Action: "*", Resource: "*", Effect: EffectAllow},
				{Action: "write_file", Resource: "*", Effect: EffectDeny},
				{Action: "git_commit", Resource: "*", Effect: EffectDeny},
				{Action: "sandbox_allow", Resource: "*", Effect: EffectDeny},
				{Action: "task", Resource: "*", Effect: EffectDeny},
				{Action: "skill_manage", Resource: "*", Effect: EffectDeny},
			},
		},
		"build": {
			Name:        "build",
			Mode:        AgentModePrimary,
			Description: "Build mode — full read/write access. Implement changes, run tests, commit code.",
			SystemPrompt: "You are TinyCode in BUILD mode. You have full access to all tools " +
				"including write_file, git_commit, and the task tool for delegating to " +
				"sub-agents. Use tools to implement changes and verify your work.",
			MaxSteps:    50,
			Permissions: Ruleset{
				{Action: "*", Resource: "*", Effect: EffectAllow},
			},
		},
		"explore": {
			Name:        "explore",
			Mode:        AgentModeSubagent,
			Description: "Fast code exploration sub-agent. Searches files, reads code. No edit permissions and no bash.",
			SystemPrompt: "You are a focused explore sub-agent. Your job is to find information " +
				"in the codebase. You can use read_file and search_files. " +
				"Return a concise summary of what you found. Do NOT modify any files.",
			MaxSteps: 15,
			Permissions: Ruleset{
				{Action: "*", Resource: "*", Effect: EffectDeny},
				{Action: "read_file", Resource: "*", Effect: EffectAllow},
				{Action: "search_files", Resource: "*", Effect: EffectAllow},
			},
		},
		"general": {
			Name:        "general",
			Mode:        AgentModeSubagent,
			Description: "General-purpose sub-agent for parallel research. Reads files, searches code, runs commands. Cannot modify files or delegate to other agents.",
			SystemPrompt: "You are a general-purpose sub-agent. Your job is to research, analyze, " +
				"and gather information. You have access to most tools including LSP, " +
				"skill loading, and search, but you CANNOT write files, commit code, " +
				"change sandbox permissions, or delegate tasks to other agents.\n\n" +
				"Return a concise summary of your findings. Do NOT produce verbose output.",
			MaxSteps: 20,
			Permissions: Ruleset{
				{Action: "*", Resource: "*", Effect: EffectAllow},
				{Action: "write_file", Resource: "*", Effect: EffectDeny},
				{Action: "git_commit", Resource: "*", Effect: EffectDeny},
				{Action: "sandbox_allow", Resource: "*", Effect: EffectDeny},
				{Action: "task", Resource: "*", Effect: EffectDeny},
				{Action: "skill_manage", Resource: "*", Effect: EffectDeny},
			},
		},
		"compact": {
			Name:        "compact",
			Mode:        AgentModePrimary,
			Hidden:      true,
			Description: "Compresses conversation history when it exceeds the token threshold.",
			SystemPrompt: "You are a conversation summarizer. Given a conversation history, " +
				"produce a concise summary (3-5 sentences) covering:\n" +
				"- What tasks were completed\n" +
				"- What decisions were made\n" +
				"- What files were modified or created\n" +
				"- Any pending issues or remaining work\n\n" +
				"Focus on facts and outcomes. Omit greetings, meta-commentary, and tool output details.",
			MaxSteps: 1,
			Permissions: Ruleset{
				{Action: "*", Resource: "*", Effect: EffectDeny},
			},
		},
		"title": {
			Name:        "title",
			Mode:        AgentModePrimary,
			Hidden:      true,
			Description: "Generates a short descriptive title for a conversation.",
			SystemPrompt: "Generate a short, descriptive title (max 50 chars) for this conversation. " +
				"Use the format: 'Topic — action'. For example: 'tmux — analyze library dependencies' " +
				"or 'Todo feature — fix rendering layout'. Return ONLY the title, nothing else.",
			MaxSteps: 1,
			Permissions: Ruleset{
				{Action: "*", Resource: "*", Effect: EffectDeny},
			},
		},
	}
}
