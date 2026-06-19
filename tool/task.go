package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/tlog"
)

// TaskToolDeps holds the dependencies the task tool needs to create sub-agents.
type TaskToolDeps struct {
	// Provider is the shared LLM provider for sub-agents.
	Provider agent.LLMProvider
	// AllTools is the full list of registered tools available for sub-agents.
	AllTools []agent.Tool
	// GetAgentConfig returns a sub-agent config by name (from DefaultAgents).
	// Returns nil if the agent is not found or is not a subagent.
	GetAgentConfig func(name string) *agent.AgentConfig
}

// TaskTool creates the task tool for delegating to sub-agents.
func TaskTool(deps *TaskToolDeps) agent.Tool {
	return agent.Tool{
		Name:        "task",
		Description: "Delegate a task to a sub-agent for focused execution. " +
			"Use explore for read-only codebase searches (files, patterns, content). " +
			"Use general for multi-step non-write work that needs bash access. " +
			"Describe the goal clearly — the sub-agent runs independently.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type":        "string",
					"description": "The sub-agent to use: explore (read-only search) or general (multi-step, has bash)",
					"enum":        []string{"explore", "general"},
				},
				"goal": map[string]any{
					"type":        "string",
					"description": "What the sub-agent should accomplish. Be specific and self-contained.",
				},
			},
			"required": []string{"agent", "goal"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["agent"].(string)
			goal, _ := args["goal"].(string)
			if name == "" || goal == "" {
				return "", fmt.Errorf("task: agent and goal are required")
			}

			// Look up sub-agent config
			cfg := deps.GetAgentConfig(name)
			if cfg == nil {
				return "", fmt.Errorf("task: unknown agent %q", name)
			}

			// Filter tools by sub-agent permissions
			var subTools []agent.Tool
			for _, t := range deps.AllTools {
				if cfg.IsToolAllowed(t.Name) {
					subTools = append(subTools, t)
				}
			}

			// Create sub-agent with independent execution
			sub := agent.New(deps.Provider)
			sub.Config = cfg
			sub.Tools = subTools
			sub.MaxSteps = cfg.MaxSteps
			sub.ShowThinking = false // suppress sub-agent reasoning in output
			sub.SessionStore = nil   // sub-agent doesn't persist to session

			tlog.Debug("task", "start", "agent", name, "goal", goal,
				"tools", len(subTools), "maxSteps", cfg.MaxSteps)

			// Run sub-agent with timeout
			type result struct {
				output string
				err    error
			}
			ch := make(chan result, 1)
			go func() {
				out, err := sub.Run(ctx, goal)
				ch <- result{out, err}
			}()

			var r result
			select {
			case r = <-ch:
			case <-time.After(120 * time.Second):
				return "", fmt.Errorf("task: sub-agent %q timed out after 120s", name)
			}

			if r.err != nil {
				tlog.Debug("task", "error", "agent", name, "err", r.err)
				if strings.Contains(r.err.Error(), "max steps") {
					if r.output != "" {
						return fmt.Sprintf("[task %q hit max steps — partial result]\n%s", name, r.output), nil
					}
					return fmt.Sprintf("[task %q hit max steps — no output]", name), nil
				}
				return "", fmt.Errorf("task: sub-agent %q failed: %w", name, r.err)
			}

			tlog.Debug("task", "done", "agent", name, "output_size", len(r.output))
			return r.output, nil
		},
	}
}
