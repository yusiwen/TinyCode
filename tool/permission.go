package tool

import "github.com/yusiwen/tinycode/agent"

// CheckToolPermission returns true if the given tool is allowed for the agent.
// It delegates to agent.ToolAllowedFor so the permission logic lives in one
// place; a nil config carries no policy and therefore permits the tool.
func CheckToolPermission(cfg *agent.AgentConfig, toolName string) bool {
	return agent.ToolAllowedFor(cfg, toolName)
}
