package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Agent is the core ReAct loop.
type Agent struct {
	Config *AgentConfig // agent mode config (plan/build/subagent)

	Provider   LLMProvider
	Tools      []Tool
	Memory     types.MemoryStore
	MemoryMode int // 0=none, 1=auto, 2=on-demand
	// Session persistence
	SessionStore interface {
		Append(msg types.Message) error
		Flush() error
	}
	History []types.Message // multi-turn conversation history

	// Compression settings
	CompressionThreshold int // token threshold to trigger compression (0 = disabled)
	ContextLength        int // model context window size (0 = unknown)

	// Discovered context length (lowered after context_length_exceeded errors)
	discoveredCtxLen int

	// TodoStorer (used by compression for active-todo injection)
	TodoStorer interface{ FormatForInjection() string }

	SystemPrompt    string
	MaxSteps        int
	MaxTokens       int
	Verbose         bool                   // when true, print detailed tool results
	ShowThinking    bool                   // when true, display reasoning_content from thinking mode
	StreamCallbacks *types.StreamCallbacks // optional streaming callbacks (TUI mode)

	// UsageTotal is every token this Agent has spent, as the provider reported
	// it. A call whose endpoint reports nothing adds an estimate of the text it
	// produced, so the total is a safe over-approximation rather than a lower
	// bound — a provider that omits usage must not silently disable a budget.
	UsageTotal types.Usage

	// Budgets bound that total, in tokens, and stop a run before it makes another
	// provider call. Zero means unlimited. BudgetTokensPerRun covers the run in
	// flight; BudgetTokensPerSession covers every run this Agent has served.
	BudgetTokensPerRun     int
	BudgetTokensPerSession int

	// runUsage is UsageTotal restricted to the run in flight, reset by each Run.
	runUsage types.Usage

	// runFailures counts, per exact tool call, how many times it has failed in the
	// run in flight. See callRefused: a fresh prompt is a fresh start.
	runFailures map[string]int

	// runSerial counts the runs this Agent has started, from 1. It is the run's
	// identity for anything that has to tell two runs apart: both begin at step
	// 1, so step and call alone do not name a tool call. The checkpoint store
	// groups one call's pre-images by (run, step, call).
	runSerial int

	// PriceTable prices a call whose route reported no charge. Nil prices
	// nothing, which is reported as an unknown cost rather than as zero.
	PriceTable *PriceTable
	// Now is the clock a declared price is resolved against. It is read once per
	// provider call, at the moment the call starts, because a route bills by the
	// hour the request was made and a long stream can cross the boundary. Nil
	// means time.Now: a test sets it to price a call at a fixed instant.
	Now func() time.Time

	// Trajectory records this agent's runs. Nil records nothing, which is how
	// "off" is spelled for every optional collaborator in this package.
	Trajectory *Trajectory
	// TrajectoryMaxRuns and TrajectoryMaxBytes bound the trajectory store, which
	// is pruned when a run closes. Zero means the package defaults.
	TrajectoryMaxRuns  int
	TrajectoryMaxBytes int64
	// TrajectoryParent, when set, makes every run of this agent a child of that
	// run: a delegated agent writes its own file, named and linked by the parent
	// that started it. The parent hands out the number, so two children of one
	// parent cannot collide.
	TrajectoryParent *TrajectoryRun

	// SessionID names the conversation a run belongs to, when one exists. It is
	// written on every trajectory line, so a run can be traced back to its
	// session; empty is legal, because the TUI mints an id after the first save
	// and a run can start before that.
	SessionID string

	// CostTotal is every cost this Agent knows, per unit, and CostUnknownCalls
	// counts the calls whose cost it does not know. They are kept together
	// because a total without its unknown count overstates what is known:
	// "0.40 USD" and "0.40 USD and three calls nobody priced" are different
	// statements.
	CostTotal        types.CostTotals
	CostUnknownCalls int

	ContentStreamed bool // true when content was streamed via SSE; skip glamour re-print

	// trajRun is the file of the run in flight, nil when nothing is recorded.
	// Every recording site checks it, so an agent with no trajectory costs no
	// clock reads and no allocations beyond this pointer.
	trajRun *TrajectoryRun
	// ttft and ttftSet carry the delay to the first streamed delta of the call in
	// flight: the loop's callback wrapper sets them, recordCall consumes them.
	ttft    time.Duration
	ttftSet bool
	// callStart is when the call in flight was sent, which is what its first
	// delta is measured from. It is the same instant the call is priced at.
	callStart time.Time
}

// callSpend is the token accounting for one provider call: what the provider
// reported, or — for an endpoint that reports nothing — an estimate of the text
// it produced. The second result says which of the two it is.
//
// The fallback covers the output only, so it undercounts the prompt, which is
// why a reported record always wins; its purpose is to keep the boundary moving
// on every call rather than only on the calls whose accounting we like.
func callSpend(resp *types.ChatResponse) (spend types.Usage, reported bool) {
	if resp.Usage != nil {
		return *resp.Usage, true
	}
	n := EstimateTokens(resp.Content) + EstimateTokens(resp.ReasoningContent)
	if n == 0 {
		return types.Usage{}, false
	}
	return types.Usage{CompletionTokens: n, TotalTokens: n}, false
}

// now reads the agent's clock, defaulting to the wall clock. One read per
// provider call, taken before the request is sent, so the call is priced by the
// hour it was made in.
func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// recordCall accounts one provider call: its tokens and its cost.
//
// Tokens: accumulated into the run and session totals, with a *reported* record
// published to the callbacks. The callback never fires for the estimate
// fallback, so no consumer is handed an estimate dressed as a provider number.
//
// Cost: a charge the route reported wins outright; otherwise a rate the user
// declared for this route and model is applied to the *reported* usage, as of
// startedAt — the instant the request was sent, which is what a time-of-day price
// is keyed on. A call whose tokens we estimated is not priced at all —
// multiplying a declared rate by a guessed token count would dress a second
// estimate as a measurement — and neither is a call with no declared rate. Both
// are counted as unknown, which is not the same as free.
func (a *Agent) recordCall(cb *types.StreamCallbacks, resp *types.ChatResponse, startedAt time.Time, step int) {
	spend, reported := callSpend(resp)
	if reported || spend.TotalTokens > 0 {
		a.UsageTotal = a.UsageTotal.Add(spend)
		a.runUsage = a.runUsage.Add(spend)
		if reported && cb != nil && cb.OnUsage != nil {
			cb.OnUsage(spend)
		}
	}

	event := types.CostEvent{Source: types.CostUnknown}
	switch {
	case resp.Cost != nil:
		event = types.CostEvent{Cost: *resp.Cost, Source: types.CostReported}
	case reported && a.PriceTable != nil:
		if key, ok := a.priceKey(); ok {
			if cost, priced := a.PriceTable.AmountAt(key, spend, startedAt); priced {
				event = types.CostEvent{Cost: cost, Source: types.CostDeclared}
			}
		}
	}

	if event.Source == types.CostUnknown {
		a.CostUnknownCalls++
	} else {
		a.CostTotal = a.CostTotal.Add(event.Cost)
	}
	if cb != nil && cb.OnCost != nil {
		cb.OnCost(event)
	}

	// The trajectory records the call's duration from the same instant the price
	// was keyed on. This is the one place the clock is read twice for a call,
	// and only when a trajectory is being written: the first read is still the
	// price's, so a route that bills by the hour is unaffected by measuring it.
	if a.trajRun != nil {
		ttft := a.ttft
		a.ttft, a.ttftSet = 0, false
		callOutcome := trajectoryOutcomeFinal
		switch {
		case len(resp.ToolCalls) > 0:
			callOutcome = trajectoryOutcomeToolCalls
		case resp.Content == "":
			callOutcome = trajectoryOutcomeEmpty
		}
		a.trajRun.LLMCall(step, a.now().Sub(startedAt), ttft, callOutcome, spend, reported, &event)
	}
}

// budgetReached reports the budget that has been spent, if any, with the numbers
// the stop message names.
//
// The session limit is checked first: once a session is out of budget no run may
// start at all, and a fresh run's own total is nowhere near its limit, so the
// session is the fact worth reporting.
func (a *Agent) budgetReached() (which string, limit, spent int, reached bool) {
	if a.BudgetTokensPerSession > 0 && a.UsageTotal.TotalTokens >= a.BudgetTokensPerSession {
		return "session", a.BudgetTokensPerSession, a.UsageTotal.TotalTokens, true
	}
	if a.BudgetTokensPerRun > 0 && a.runUsage.TotalTokens >= a.BudgetTokensPerRun {
		return "run", a.BudgetTokensPerRun, a.runUsage.TotalTokens, true
	}
	return "", 0, 0, false
}

// budgetMessage is what a run returns when it stops at a budget. It is an
// outcome to read, like the step-limit summary, rather than an error: the user
// asked for a bound and got it, and the message says which knob to turn.
func budgetMessage(which string, limit, spent int) string {
	return fmt.Sprintf("Stopped at the %s token budget: %d of %d tokens spent. "+
		"Raise budget.max_tokens_per_%s to continue.", which, spent, limit, which)
}

// ANSI color codes for terminal output.
const (
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
	colorYellow = "\033[33m"
	colorDim    = "\033[2m"
	colorReset  = "\033[0m"

	thinkingPrefix = "| "
)

// agentPrefix returns the display prefix based on current mode config.
func (a *Agent) agentPrefix() string {
	if a.Config != nil {
		return "[" + a.Config.Name + "]"
	}
	return "[tinycode]"
}

// stepName prints the step header (always visible) in cyan.
// In TUI mode (StreamCallbacks set), this is handled by the TUI renderer.
func (a *Agent) stepName(format string, args ...any) {
	if a.StreamCallbacks != nil {
		return // TUI mode — direct stdout bypasses Bubble Tea
	}
	fmt.Print("\n" + colorCyan + a.agentPrefix() + " " + fmt.Sprintf(format, args...) + colorReset + "\n")
}

// stepDetail prints detailed output in gray, only when Verbose is enabled.
func (a *Agent) stepDetail(format string, args ...any) {
	if a.StreamCallbacks != nil {
		return
	}
	if a.Verbose {
		fmt.Print(colorGray + a.agentPrefix() + " " + fmt.Sprintf(format, args...) + colorReset + "\n")
	}
}

const (
	MemoryModeNone     = 0
	MemoryModeAuto     = 1
	MemoryModeOnDemand = 2
)

// New creates an Agent with sensible defaults.
func New(provider LLMProvider) *Agent {
	return &Agent{
		Provider: provider,
		SystemPrompt: "You are TinyCode, an AI coding assistant. " +
			"Use tools when needed to accomplish the user's request. " +
			"Think step by step. You have a limited budget of 20 tool calls " +
			"per request — plan which files to read strategically. " +
			"Use bash (tree/find) to explore project structure first, " +
			"then read only the key files needed to answer.",
		MaxSteps:  20,
		MaxTokens: 4096,
	}
}

// AddTool registers a tool.
func (a *Agent) AddTool(t Tool) {
	a.Tools = append(a.Tools, t)
}

// getModel returns the agent's model override, or empty string to use provider default.
func (a *Agent) getModel() string {
	if a.Config != nil {
		return a.Config.Model
	}
	return ""
}

// recordTurn adds one finished turn to the model's multi-turn history and to
// the session log.
//
// It exists because three endings — the step limit, a budget and a terminal
// refusal — used to return without recording anything, so the next run (a
// human's "continue", or a goal round) could not read what this one did
// (issue #258). The normal final answer goes through the same path so the two
// cannot drift.
//
// History keeps the reasoning the model produced; the session log does not, as
// it never did. A turn with no assistant text is not recorded: there is nothing
// a later run could read.
func (a *Agent) recordTurn(user, assistant, reasoning string) {
	if assistant == "" {
		return
	}
	a.History = append(a.History,
		types.Message{Role: types.RoleUser, Content: user},
		types.Message{Role: types.RoleAssistant, Content: assistant, ReasoningContent: reasoning},
	)
	if a.SessionStore == nil {
		return
	}
	a.SessionStore.Append(types.Message{Role: types.RoleUser, Content: user})
	a.SessionStore.Append(types.Message{Role: types.RoleAssistant, Content: assistant})
	if err := a.SessionStore.Flush(); err != nil {
		log.Printf("warning: flush session: %v", err)
	}
}

// Run executes the ReAct loop for a user prompt.
func (a *Agent) Run(ctx context.Context, prompt string) (answer string, runErr error) {
	a.runSerial++

	// A run is recorded from its first instant, so the file's duration covers
	// everything it did — compression included — rather than only the model
	// calls. The cost totals are captured the same way: CostTotal is replaced on
	// every call rather than mutated, so holding the value from here gives the
	// run's share by subtraction at the end.
	runStarted := time.Time{}
	var costStart types.CostTotals
	unknownStart := 0
	outcome := runOutcome{stop: trajectoryStopFinal}
	if a.recording() {
		runStarted = a.now()
		costStart = a.CostTotal
		unknownStart = a.CostUnknownCalls
		a.trajRun = a.beginTrajectory(prompt)
	}
	defer func() {
		stop := outcome.stop
		switch {
		case ctx.Err() != nil:
			// A cancelled provider call also returns an error; the cancel is the
			// fact worth recording, because it says who stopped the run.
			stop = trajectoryStopCancelled
		case runErr != nil:
			stop = trajectoryStopError
		}
		a.finishTrajectory(stop, outcome.steps, runStarted, costStart, unknownStart)
	}()
	// Resolve system prompt from config, falling back to Agent.SystemPrompt
	sysPrompt := a.SystemPrompt
	if a.Config != nil && a.Config.SystemPrompt != "" {
		sysPrompt = a.Config.SystemPrompt
	}
	messages := []types.Message{
		{Role: types.RoleSystem, Content: sysPrompt},
	}

	// Load multi-turn history, skipping messages that would cause API errors
	// Compress history if it exceeds the threshold
	compressed, err := a.compressHistory(ctx, a.History)
	if err == nil && compressed != nil {
		a.History = compressed
	}
	for _, msg := range a.History {
		// Skip assistant messages with neither content nor tool_calls
		if msg.Role == types.RoleAssistant && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		messages = append(messages, msg)
	}

	// Inject long-term memory if enabled
	if a.Memory != nil && a.MemoryMode == MemoryModeAuto {
		memories, err := a.Memory.Recall(prompt, 5)
		if err == nil && len(memories) > 0 {
			var sb string
			for _, m := range memories {
				sb += fmt.Sprintf("- %s: %s\n", m.Key, m.Value)
			}
			messages = append(messages, types.Message{
				Role:    types.RoleSystem,
				Content: "Relevant memories:\n" + sb,
			})
		}
	}

	messages = append(messages, types.Message{Role: types.RoleUser, Content: prompt})

	step := 0
	// Resolve max steps from config, falling back to Agent.MaxSteps
	maxSteps := a.MaxSteps
	if a.Config != nil && a.Config.MaxSteps > 0 {
		maxSteps = a.Config.MaxSteps
	}

	// Freeze this run's file-effect policy onto its context, not into package
	// state, so concurrent runs — including sub-agents — cannot change a mode
	// or a writable root for each other. A policy already on the context wins:
	// a caller that set a boundary meant it, and a nested run must not widen it
	// by starting with one of its own.
	if _, ok := types.SandboxPolicyFrom(ctx); !ok {
		mode := types.SandboxWorkspaceWrite
		if a.Config != nil && a.Config.Name == "plan" {
			mode = types.SandboxReadOnly
		}
		projectRoot, roots := types.ResolveSandboxRoots(mode)
		ctx = types.WithSandboxPolicy(ctx, types.SandboxPolicy{
			Mode:        mode,
			ProjectRoot: projectRoot,
			Roots:       roots,
			Source:      "agent " + a.agentPrefix(),
		})
	}

	// The run's own spend is measured from here, whatever the session has already
	// spent. A nested run (a sub-agent) has its own Agent and so its own totals.
	a.runUsage = types.Usage{}
	// So is the record of which calls have already failed this run.
	a.runFailures = nil

	for step < maxSteps {
		tlog.Info("agent.loop", "llm call", "step", step, "mode", a.agentPrefix())

		// A budget is enforced here, before the call that would spend, because
		// this is the only place in the loop that spends. Checking after the call
		// would let every run overshoot by one call; the call that crossed the
		// line is already counted, and the message reports the real numbers.
		if which, limit, spent, reached := a.budgetReached(); reached {
			msg := budgetMessage(which, limit, spent)
			outcome.stop = trajectoryStopBudget
			tlog.Info("agent.loop", "budget reached", "budget", which, "limit", limit, "spent", spent, "steps", step)
			a.recordTurn(prompt, msg, "")
			return msg, nil
		}

		// The outcome counts the step that is about to make a model call, not
		// the ones completed: a run that stops inside step 2 has still done two
		// steps, while a run stopped by the budget check above has done none.
		outcome.steps = step + 1

		// Build tool definitions, filtering by config permissions
		toolDefs := make([]types.ToolDef, 0, len(a.Tools))
		for _, t := range a.Tools {
			if a.Config != nil && !ToolAllowedFor(a.Config, t.Name) {
				continue // skip tools not allowed in current mode
			}
			toolDefs = append(toolDefs, types.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		// Determine streaming callbacks: use Agent-level if set (TUI mode),
		// otherwise create default callbacks for terminal display.
		callbacks := a.StreamCallbacks
		tlog.Debug("agent.loop", "callbacks_check", "step", step, "has_callbacks", callbacks != nil)
		if callbacks == nil {
			var reasoningFirstToken bool
			callbacks = &types.StreamCallbacks{
				OnReasoningDelta: func(text string) {
					if a.ShowThinking {
						if !reasoningFirstToken {
							reasoningFirstToken = true
							fmt.Print(colorDim + colorYellow + thinkingPrefix)
						}
						fmt.Print(text)
					}
				},
				OnTextDelta: func(text string) {
					fmt.Print(colorReset + text)
				},
				OnRetry: func(attempt, maxAttempts int, err error) {
					// A backoff is a pause, and a pause with no line printed
					// looks like a hang.
					fmt.Printf("\n%sretrying (%d/%d): %v%s\n", colorYellow, attempt, maxAttempts, err, colorReset)
				},
			}
		}

		// The step is timed and the first delta captured only when something is
		// recording: a reader of the #225 invariant is entitled to the clock
		// being read once per call when no trajectory is being written.
		stepStarted := time.Time{}
		if a.trajRun != nil {
			stepStarted = a.now()
			callbacks = a.recordFirstDelta(callbacks)
		}

		// Call provider
		started := a.now()
		a.callStart = started
		resp, err := a.Provider.Chat(ctx, types.ChatRequest{
			Messages:        messages,
			Tools:           toolDefs,
			MaxTokens:       a.MaxTokens,
			Model:           a.getModel(),
			StreamCallbacks: callbacks,
		})
		if err != nil {
			tlog.Error("agent.loop", "llm error", "step", step, "error", err)
			a.HandleContextError(err)
			return "", fmt.Errorf("LLM call failed: %w", err)
		}
		a.recordCall(callbacks, resp, started, step)

		// Reasoning already handled by streaming callback (OnReasoningDelta)
		if a.ShowThinking {
			fmt.Print(colorReset)
		}

		// No tool calls → final answer
		if len(resp.ToolCalls) == 0 {
			// Degenerate case: empty content with no tool calls
			// LLM spent all tokens on reasoning and produced nothing.
			if resp.Content == "" {
				tlog.Warn("agent.loop", "empty_response", "step", step, "mode", a.agentPrefix(), "reasoning_len", len(resp.ReasoningContent))
				// Continue the loop to retry rather than returning empty
				messages = append(messages, types.Message{
					Role:    types.RoleAssistant,
					Content: "(model produced no output after thinking)",
				})
				step++
				continue
			}

			tlog.Info("agent.loop", "answer", "step", step, "mode", a.agentPrefix(), "resp_len", len(resp.Content))
			a.ContentStreamed = true
			// Save to multi-turn history (skip empty responses)
			if resp.Content != "" {
				a.recordTurn(prompt, resp.Content, resp.ReasoningContent)
			}
			if a.trajRun != nil {
				a.trajRun.StepEnd(step, a.now().Sub(stepStarted))
			}
			return resp.Content, nil
		}

		// Multiple tool calls in one step
		toolCalls := resp.ToolCalls
		tlog.Debug("agent.loop", "tool calls", "step", step, "count", len(toolCalls))

		// Build tool names string for step header
		names := make([]string, len(toolCalls))
		for i, tc := range toolCalls {
			names[i] = tc.Name
		}
		a.stepName("[step %d] calling tools: %s", step, strings.Join(names, ", "))

		// Append assistant message with ALL tool calls
		assistantMsg := types.Message{
			Role:             types.RoleAssistant,
			Content:          "",
			ReasoningContent: resp.ReasoningContent,
		}
		assistantMsg.ToolCalls = make([]types.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			assistantMsg.ToolCalls[i] = types.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			}
		}
		messages = append(messages, assistantMsg)

		// Execute each tool call and collect results
		for _, tc := range toolCalls {
			tlog.Debug("agent.loop", "tool_check", "step", step, "tool", tc.Name, "has_callbacks", callbacks != nil, "has_ontoolcall", callbacks != nil && callbacks.OnToolCall != nil)
			if callbacks != nil && callbacks.OnToolCall != nil {
				// Extract a short argument summary for display
				argSummary := ""
				if tc.Arguments != "" {
					var raw map[string]any
					if err := json.Unmarshal([]byte(tc.Arguments), &raw); err == nil {
						for _, v := range raw {
							if s, ok := v.(string); ok {
								argSummary = s
								break
							}
						}
					}
					if argSummary == "" && len(tc.Arguments) > 60 {
						argSummary = tc.Arguments[:60] + "..."
					}
				}
				callbacks.OnToolCall(tc.Name, argSummary)
			}
		}
		tlog.Info("agent.loop", "tool calls", "step", step, "count", len(toolCalls))

		// Execute all tool calls concurrently
		type toolResult struct {
			Index     int
			Name      string
			Args      string
			Result    string
			Truncated string
			IsBlock   bool
			Duration  time.Duration
		}
		ch := make(chan toolResult, len(toolCalls))
		for idx, tc := range toolCalls {
			idx, tc := idx, tc
			go func() {
				a.stepDetail("[step %d] calling %s: %s", step, tc.Name, tc.Arguments)
				tlog.Info("agent.loop", "tool exec", "step", step, "tool", tc.Name)
				toolStarted := time.Time{}
				if a.trajRun != nil {
					toolStarted = a.now()
				}

				var result string
				// A panicking tool must not take down the whole agent: convert
				// it into an error result for this tool call only.
				func() {
					defer func() {
						if r := recover(); r != nil {
							result = fmt.Sprintf("error: tool %s panicked: %v", tc.Name, r)
							tlog.Error("agent.loop", "tool panic", "tool", tc.Name,
								"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
						}
					}()

					found := false
					for _, t := range a.Tools {
						if t.Name == tc.Name {
							if a.Config != nil && !ToolAllowedFor(a.Config, t.Name) {
								result = fmt.Sprintf("[DENIED] %s is not available in %s mode.", t.Name, a.Config.Name)
								found = true
								break
							}
							found = true
							var args map[string]any
							if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
								// Unparseable arguments can loop as surely as a failing
								// call, so they are counted under the raw text.
								key := rawCallKey(t.Name, tc.Arguments)
								if a.callRefused(key) {
									result = repeatMessage(t.Name, a.runFailures[key])
								} else {
									a.noteCallFailure(key)
									result = fmt.Sprintf("error parsing args: %v", err)
								}
								break
							}
							// One identity per call, so a model repeating a failing call
							// is recognised even when the JSON key order differs.
							key := toolCallKey(t.Name, args)
							if a.callRefused(key) {
								result = repeatMessage(t.Name, a.runFailures[key])
							} else if problems := ValidateArgs(t.Parameters, args); len(problems) > 0 {
								// Not executed: the schema the model was shown is the
								// contract, and a call that breaks it is answered with
								// what is wrong rather than with whatever the tool's own
								// parsing happened to say.
								a.noteCallFailure(key)
								result = InvalidArgsMessage(t.Name, problems)
							} else {
								var execErr error
								// The call's own scope travels with it: a layer below
								// that records what a tool touched (the checkpoint
								// store) needs to attribute it to this call, and two
								// runs in one process reuse step 1, so the run
								// counter is what keeps them apart.
								//
								// The scope is extended rather than replaced: the
								// caller seeds the session (the TUI mints it on its
								// first save, so the loop cannot know it), and the
								// fields below are the ones only the loop can fill.
								scope, _ := types.ToolCallScopeFrom(ctx)
								scope.Tool, scope.Run, scope.Step, scope.Call = t.Name, a.runSerial, step, idx
								result, execErr = t.Execute(types.WithToolCallScope(ctx, scope), args)
								if execErr != nil {
									a.noteCallFailure(key)
									result = fmt.Sprintf("error: %v", execErr)
								}
							}
							break
						}
					}
					if !found {
						result = fmt.Sprintf("unknown tool: %s", tc.Name)
					}
				}()

				if callbacks != nil && callbacks.OnToolResult != nil {
					callbacks.OnToolResult(tc.Name)
				}

				// A terminal refusal is one nothing the model tries can change
				// — a capability the machine lacks, or a configured rule. The
				// loop reports it instead of spending another model turn on it.
				isBlock := types.RefusalIsTerminal(result)

				trunc := TruncateOutput(result)

				toolDuration := time.Duration(0)
				if a.trajRun != nil {
					toolDuration = a.now().Sub(toolStarted)
				}
				ch <- toolResult{
					Index:     idx,
					Name:      tc.Name,
					Args:      tc.Arguments,
					Result:    result,
					Truncated: trunc.Content,
					IsBlock:   isBlock,
					Duration:  toolDuration,
				}
			}()
		}

		// Collect results in index order
		results := make([]toolResult, len(toolCalls))
		for range toolCalls {
			r := <-ch
			results[r.Index] = r
		}

		// Process results in order
		for _, r := range results {
			tlog.Debug("agent.loop", "tool result", "step", step, "tool", r.Name, "size", len(r.Result))

			if len(r.Result) > 500 {
				a.stepDetail("[step %d] tool result (%s, %d chars):\n%s...", step, r.Name, len(r.Result), r.Result[:500])
			} else {
				a.stepDetail("[step %d] tool result (%s, %d chars):\n%s", step, r.Name, len(r.Result), r.Result)
			}

			if r.IsBlock {
				outcome.stop = trajectoryStopBlocked
				if a.trajRun != nil {
					// The step ends here rather than at the loop's tail, and the
					// calls after this one in the same step were never written:
					// a terminal refusal stops the run, which is the fact the
					// record carries.
					a.trajRun.StepEnd(step, a.now().Sub(stepStarted))
				}
				a.stepName("[step %d] security block detected, bypassing LLM", step)
				a.recordTurn(prompt, r.Result, "")
				return r.Result, nil
			}

			messages = append(messages, types.Message{
				Role:       types.RoleTool,
				Content:    r.Truncated,
				Name:       r.Name,
				ToolCallID: toolCalls[r.Index].ID,
			})
		}
		// The step is written in call order, not completion order: the calls ran
		// concurrently, and a file whose lines move between runs is not one an
		// evaluator can diff.
		if a.trajRun != nil {
			for _, r := range results {
				a.trajRun.ToolCall(step, r.Index, r.Name, r.Args, r.Result, r.Duration, toolStatus(r.Result))
			}
			a.trajRun.StepEnd(step, a.now().Sub(stepStarted))
		}

		// Step boundary — signal the TUI to prepare a new message for the next step
		if callbacks != nil && callbacks.OnStepDone != nil {
			callbacks.OnStepDone()
		}
		step++
	}

	// Max steps reached: inject forced summary
	outcome.stop = trajectoryStopStepLimit
	tlog.Warn("agent.loop", "max steps", "steps", maxSteps)
	messages = append(messages, types.Message{
		Role:    types.RoleUser,
		Content: fmt.Sprintf("You have reached the maximum step limit (%d steps). No more tool calls are allowed. Please summarize what you have accomplished so far and what remains to be done.", maxSteps),
	})
	// Force one more LLM call with no tools available
	summaryCallbacks := &types.StreamCallbacks{
		OnReasoningDelta: func(text string) {},
		OnTextDelta: func(text string) {
			if a.StreamCallbacks != nil && a.StreamCallbacks.OnTextDelta != nil {
				a.StreamCallbacks.OnTextDelta(text)
			} else {
				fmt.Print(text)
			}
		},
		OnUsage: func(usage types.Usage) {
			if a.StreamCallbacks != nil && a.StreamCallbacks.OnUsage != nil {
				a.StreamCallbacks.OnUsage(usage)
			}
		},
		OnCost: func(event types.CostEvent) {
			if a.StreamCallbacks != nil && a.StreamCallbacks.OnCost != nil {
				a.StreamCallbacks.OnCost(event)
			}
		},
		OnRetry: func(attempt, maxAttempts int, err error) {
			if a.StreamCallbacks != nil && a.StreamCallbacks.OnRetry != nil {
				a.StreamCallbacks.OnRetry(attempt, maxAttempts, err)
			}
		},
	}
	started := a.now()
	resp, err := a.Provider.Chat(ctx, types.ChatRequest{
		Messages:        messages,
		Tools:           nil, // no tools — LLM must output text only
		MaxTokens:       a.MaxTokens,
		Model:           a.getModel(),
		StreamCallbacks: summaryCallbacks,
	})
	if err != nil {
		return "", fmt.Errorf("step limit summary failed: %w", err)
	}
	a.recordCall(summaryCallbacks, resp, started, step)
	a.recordTurn(prompt, resp.Content, resp.ReasoningContent)
	return resp.Content, nil
}

// CompressHistory compresses a.History in-place using the agent's provider for
// summarization. The caller's context bounds the summarization call, so a
// cancel ends a stalled summarizer instead of waiting for the provider's own
// timeout (which the Ollama provider does not have).
//
// It reports whether the history was replaced and returns the summarizer error,
// if any. On error or cancellation a.History is left untouched, so a caller can
// retry with the transcript it already had.
func (a *Agent) CompressHistory(ctx context.Context) (bool, error) {
	before := len(a.History)
	compressed, err := a.compressHistory(ctx, a.History)
	if err != nil {
		return false, err
	}
	if compressed == nil {
		return false, nil
	}
	a.History = compressed
	return len(a.History) < before, nil
}
