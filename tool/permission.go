package tool

import "github.com/yusiwen/tinycode/agent"

// CheckToolPermission returns true if the given tool is allowed for the agent.
// This is called in the agent loop BEFORE tool execution.
// The function delegates to agent.ToolAllowedFor so the logic lives in one place.
func CheckToolPermission(cfg *agent.AgentConfig, toolName string) bool {
	return agent.ToolAllowedFor(cfg, toolName)
}
