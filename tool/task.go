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
	// BgTaskMgr is the shared background task manager.
	BgTaskMgr *BackgroundTaskManager
}

// TaskTool creates the task tool for delegating to sub-agents.
func TaskTool(deps *TaskToolDeps) agent.Tool {
	return agent.Tool{
		Name:        "task",
		Description: "Delegate a unit of work to a sub-agent for focused execution. " +
			"The sub-agent runs independently and its step count does not count against your step budget.\n\n" +
			"Use this when a task involves multiple independent work items " +
			"(e.g. creating separate modules, searching multiple patterns, or running independent builds). " +
			"Each task call counts as ONE external step regardless of how many internal steps the sub-agent uses.\n\n" +
			"Available sub-agents:\n" +
			"- explore: Read-only codebase searches. Use for finding files, searching patterns, reading code.\n" +
			"- general: Full execution with bash, write, edit. Use for creating files, running commands, modifying code.\n\n" +
			"Modes:\n" +
			"- Sync (default): task({agent:'general', goal:'Create module A'}) — blocks until complete\n" +
			"- Background: task({agent:'general', goal:'Build module B', bg:true}) — returns task_id immediately; " +
			"use task_collect({id: 'task_1'}) to retrieve the result later",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type":        "string",
					"description": "The sub-agent to use: explore (read-only) or general (full execution)",
					"enum":        []string{"explore", "general"},
				},
				"goal": map[string]any{
					"type":        "string",
					"description": "What the sub-agent should accomplish. Be specific and self-contained.",
				},
				"bg": map[string]any{
					"type":        "boolean",
					"description": "Run in background (default: false). When true, returns task_id immediately; use task_collect to get results.",
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

			// Background mode
			if bg, _ := args["bg"].(bool); bg && deps.BgTaskMgr != nil {
				taskID := deps.BgTaskMgr.Start(deps, name, goal)
				return fmt.Sprintf("[task %s started] %s — %s\nUse task_collect({id: %q}) to retrieve the result.",
					taskID, name, goal, taskID), nil
			}

			// Synchronous mode (original behavior)
			cfg := deps.GetAgentConfig(name)
			if cfg == nil {
				return "", fmt.Errorf("task: unknown agent %q", name)
			}
			var subTools []agent.Tool
			for _, t := range deps.AllTools {
				if cfg.IsToolAllowed(t.Name) {
					subTools = append(subTools, t)
				}
			}
			tlog.Debug("task", "agent_label", "agent", name)
			SetAgentLabel(name)
			sub := agent.New(deps.Provider)
			sub.Config = cfg
			sub.Tools = subTools
			sub.MaxSteps = cfg.MaxSteps
			sub.ShowThinking = false
			sub.SessionStore = nil

			tlog.Debug("task", "start", "agent", name, "goal", goal,
				"tools", len(subTools), "maxSteps", cfg.MaxSteps)

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

// TaskCollectTool creates the tool for collecting background task results.
func TaskCollectTool(mgr *BackgroundTaskManager) agent.Tool {
	return agent.Tool{
		Name:        "task_collect",
		Description: "Retrieve results from background tasks started with task(bg=true). " +
			"Blocks until the specified task completes. " +
			"Use this after launching multiple background tasks to collect their results.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Task ID to collect (e.g., task_1). Use a single ID per call.",
				},
			},
			"required": []string{"id"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			if id == "" {
				return "", fmt.Errorf("task_collect: id is required")
			}
			result, err := mgr.Collect(id)
			if err != nil {
				return "", err
			}
			return result, nil
		},
	}
}
