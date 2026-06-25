# TinyCode — AI coding agent in Go

## Language Policy
All code comments and documentation must be written in **English**, unless explicitly asked to use Chinese.

## Quick Start

```bash
make build          # build to bin/tinycode
make run PROMPT="..."  # build + run in one-shot mode
make test           # run all tests
make lint           # go vet + staticcheck
```

Run interactively:
```bash
./bin/tinycode                          # TUI mode
./bin/tinycode "refactor the parser"    # one-shot mode
./bin/tinycode --list-sessions          # list saved sessions
./bin/tinycode --resume=TUI-20260607-235959  # resume session
```

## Project Structure

```
agent/          ReAct loop, LLM providers, context compression, registry
config/         JSON config loading (default → ~/.tinycode/ → ./.tinycode/)
lsp/            LSP client (gopls, pyright, etc.), diagnostics, 4 tools
mcp/            MCP client (stdio/HTTP), JSON-RPC 2.0, auto-discovery
session/        Session persistence (JSON), fork/branch support, export
skill/          SKILL.md discovery (3 layers: builtin/global/project)
tlog/           Structured logger (file + level filtering)
tool/           24 tool definitions (bash, edit, git, web, LSP, task, MCP, ...)
tui/            Bubble Tea TUI (CellGrid, markdown render, cmd palette)
types/          Shared types (Message, ToolCall, ChatRequest, StreamCallbacks)
main.go         CLI entry (cobra), dependency wiring
```

## Architecture Overview

```
User Input → TUI / CLI
  → Agent.Run() (ReAct loop)
    → LLM Provider (streaming SSE, reasoning + text deltas)
    → Tool execution (concurrent goroutines, permissions checked per-call)
    → No tool calls → final answer
  → Stream callbacks → TUI incremental render (CellGrid)
```

### Agent Loop (`agent/agent.go`)
- `Run(ctx, prompt)` — core ReAct loop with step budgeting
- Compresses history at 50% of context window (head + LLM-summarized middle + tail)
- Supports multi-turn history, tool call parallel execution, security block detection
- 6 named agents managed by `Registry` (plan/build/explore/general/compact/title)

### Providers (`agent/provider*.go`)
- `LLMProvider` interface: `Chat(ctx, ChatRequest) → ChatResponse`
- OpenAI-compatible (`agent/provider_openai.go`) — streaming SSE, tool calls
- Ollama (`agent/provider_ollama.go`) — local LLMs
- MockLLM (`agent/mock_llm.go`) — scripted responses for testing
- `ProviderRegistry` — runtime switch via Tab key

### Tools (`tool/`)
Each tool is `{Name, Description, Parameters (JSON Schema), Execute(ctx, args)}`.
24 built-in tools across categories: shell, file r/w, search, edit, git, web, LSP, task, todo, skill, sandbox.

### TUI (`tui/`)
- Bubble Tea framework with custom CellGrid frame buffer
- Incremental markdown rendering (~2.3ms), reasoning folding, char-level selection
- Status bar, command palette, permission dialog, todo display
- Theme system (default/nord)

## Code Conventions

- **Language**: Go 1.24, no external code generators
- **Imports**: stdlib first, then third-party, then internal (grouped by blank lines)
- **Error handling**: return `fmt.Errorf("context: %w", err)` with lowercase message
- **Testing**: `_test.go` alongside source, `MockLLM` for agent loop tests
- **Comments**: English only unless user explicitly requests Chinese
- **Configuration**: struct tags `json:"field_name,omitempty"` with snake_case

## Dependencies

- `bubbletea` / `bubbles` — TUI framework
- `lipgloss` — ANSI styling
- `go-openai` — LLM provider
- `goldmark` — markdown parsing
- `cobra` — CLI
- `go-rod` — headless Chromium (web_extract fallback)
- `rod` — headless browser for web extraction

## Key Design Decisions

1. **No external monocle/renderer** — custom CellGrid frame buffer renders markdown directly
2. **Concurrent tool execution** — multiple tool calls per step run in goroutines
3. **Permissions engine** — Ruleset with last-match-wins, wildcard `*`/`?` support
4. **MCP first-class** — native stdio/HTTP MCP client, no SDK dependency
5. **SSRF protection** — DNS resolution + IP blacklist for HTTP transports
6. **7 fuzzy edit strategies** — exact → trimmed → ws-normalized → indent → escape → unicode → block-anchor
7. **5-level web extract fallback** — HTTP → Cloudflare → Google Cache → Wayback → Chromium
