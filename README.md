<p align="center">
  <img src="https://img.shields.io/badge/TinyCode-v0.0.3-%23FFD700?style=for-the-badge" alt="TinyCode v0.0.3"/>
  <img src="https://img.shields.io/badge/Go-1.24-%2300ADD8?style=for-the-badge&logo=go" alt="Go 1.24"/>
  <img src="https://img.shields.io/badge/License-MIT-%23green?style=for-the-badge" alt="MIT License"/>
</p>

<p align="center">
  <b>AI Coding Agent with CellGrid TUI &mdash; markdown-native rendering, reasoning folding, tool call display, session persistence</b>
</p>

<p align="center">
  <img src="https://img.shields.io/github/last-commit/yusiwen/tinycode?style=flat-square"/>
  <img src="https://img.shields.io/github/actions/workflow/status/yusiwen/TinyCode/main.yml?style=flat-square&amp;label=build" alt="Build and Test"/>
  <img src="https://img.shields.io/github/repo-size/yusiwen/tinycode?style=flat-square"/>
  <img src="https://img.shields.io/badge/tests-359-%23success?style=flat-square"/>
</p>

---

# Features

### Markdown Rendering in TUI
Custom **CellGrid** frame-buffer renders markdown directly in the terminal — no external renderer dependency.

- **Bold**, *italic*, `inline code`, headings, blockquotes, ordered/unordered lists (nested arbitrary depth)
- Fenced code blocks with `\t` → spaces conversion, indentation preservation
- Tables with smart column-width alignment, box-drawing characters (`│ ─ ┤ ├ ┼`)
- Horizontal rules, links, mixed content
- **Incremental streaming**: `parseMarkdown()` on every render tick — no sudden jump from raw markdown to styled output
- 10+ roundtrip tests verify text content survives parse → render → extract

### Session & Persistence
- Auto-save conversations on exit (`~/.tinycode/sessions/TUI-*.json`), with title/preview/metadata
- Resume with `--resume=TUI-20260607-235959`
- List sessions with `--list-sessions` (shows title, message count, last active time)
- **AI-generated session titles**: title hidden agent auto-names conversations via LLM. Falls back to first user message. (2deee5e)
- Input history: Up/Down arrows to recall previous inputs, Esc to exit browse mode
- Status bar shows `hist 3/5` during history browsing

### TUI Features
- **Incremental CellGrid Rendering** — msgDirty/msgRowCount tracking. View() skips unchanged messages, only re-renders from first dirty onward. CellGrid no longer Reset() every frame. Benchmark: ~2.3ms regardless of message count (10/50/100 tested). (ffbbf22)
- **Reasoning folding**: click `[+]`/`[-]` markers to expand/collapse LLM reasoning blocks
- **Tool call display**: `→ Calling tools:` with bullet list and `⏳ Running...` indicator during tool execution
- **Character-level selection**: drag-select any range of viewport text, Ctrl+C copies rendered text (not raw markdown)
- **[Copy] buttons**: click to copy assistant response to clipboard
- **Response label**: gold bold `Response:` with blank line before for visual separation
- **Auto-scroll**: viewport follows streaming output, pauses when user scrolls up
- **Status bar**: mode icon, model name, provider, token/tool/msg counts, session duration, transient status messages

### Agent Loop
- ReAct loop with tool calling support (bash, read_file, search_files, git, LSP tools)
- **6 agents**: plan (read-only), build (full access), explore (3 tools), general (all except write), compact (history compression), title (session naming)
- Agent integration test framework: 13 tests using MockLLM step-by-step
- Streaming reasoning + text deltas
- Tool call lifecycle displayed in real-time
- Multi-turn history compression: Hermes-style head/tail/middle summarization
- 1M token context (DeepSeek V4 Flash) with automatic threshold lowering on `context_length_exceeded` errors

### LSP Integration
- **Config**: `"lsp": { "enabled": true }` in config.json (default: disabled). 7 supported languages with auto-detection: Go (`gopls`), Python (`pyright`), TypeScript/JS (`typescript-language-server`), Rust (`rust-analyzer`), C++ (`clangd`), Java (Eclipse JDT).
- **4 tools** exposed to LLM: `lsp_definition` (→ `Definition at path:line:col`), `lsp_references` (→ `Found N references`), `lsp_hover` (→ type info + docs), `lsp_symbols` (→ all symbols in file). All require `file_path`, `line`, `character` (0-indexed).
- **Architecture**: Each call starts a fresh LSP process: `exec.Command("gopls")` → stdin/stdout pipes → JSON-RPC with Content-Length framing → `Initialize` → `StartReader()` (background diagnostics listener) → tool operation → `Close()`.
- **Incremental Diagnostics** — `SnapshotBaseline` captures diagnostic state before edit/write_file/apply_patch, `GetNewDiagnostics` computes delta. Tools report only new errors via LSP — LLM sees focused feedback. (a2e3e07)
- **TUI Error Tracking** — LSPDiagMsg carries per-file diagnostic sets. Status bar shows `errors: N` with live count. `/diagnostics` command lists all current file errors in viewport. (290818a)
- **Mock test framework** — `io.Pipe` based, no LSP server required, 15+ tests covering all 4 tool types. (8065ae5)
- **Limitation**: Per-call process startup (~500ms overhead). Not a persistent LSP connection despite the long-lived design intent.

### Todo System
- **TodoStore**: In-memory task list with CRUD (create/read/update/merge/delete/summary). Enforces one `in_progress`, max 256 items, max 4000 chars per task.
- **todo tool**: OpenAI function-calling schema, registered in all modes. LLM calls it to plan and track multi-step work.
- **Compression protection**: After context compression, active todo items (pending + in_progress) are re-injected so the LLM doesn't redo completed work.
- **Housekeeping mute**: When all tool calls are `todo`, the model's text reply is suppressed — no noise, just progress markers.
- **Session recovery**: On `--resume`, reverse-scans history for the latest todo result and restores the store.
- **TUI rendering**: `▾ Todo (2/6)` with `[x]` completed, `[>]` in_progress, `[ ]` pending, `[~]` cancelled markers.

### Line-Level Code Edit
- **edit tool**: Search/replace editing (old_string + new_string). **7 fuzzy strategies** (exact → line-trimmed → ws-normalized → indent-flexible → escape-normalized → unicode-normalized → block-anchor) + indentation correction. Validates uniqueness. Multiple edits per call. LSP integration. 14 tests. (d067156, 34d2c17)
- **apply_patch tool**: V4A multi-file patch format. Supports UPDATE (line-level -/+ hunks), ADD (create files), DELETE (remove files). Two-phase execution: validate all, then apply. Multi-file in one call. 9 tests. (9045176)
- **write_file** preserved for creating new files and full rewrites. Three tools form a complementary editing system.

### MCP Client
- **Native MCP client**: Connect to MCP servers via stdio (subprocess) or HTTP (remote endpoint). Config-driven via `mcp_servers` in config.json.
- **Auto-discovery**: On startup, connects to all servers, calls `tools/list`, and registers each discovered tool as an independent `mcp_<server>_<tool>` agent tool with its original JSON Schema.
- **Transport**: stdio (exec.Command with pipes, Content-Length framing) or HTTP POST (configurable headers, JSON-RPC 2.0).
- **Resources**: `resources/list` and `resources/read` support for MCP resources.
- **Security**: SSRF protection for HTTP transport — blocks private IPs (RFC 1918/loopback/link-local), fails closed on DNS failure. Localhost allowed for dev use.
- **Graceful degradation**: Server connection failure logs a warning and skips that server — other servers still work. 22 tests. 359 total. (e31c08b, cc1ba8e, 7b4fe75)

### Web Tools
- **web_search**: Searches the web using DuckDuckGo Lite (zero config, no API key). Optional SearXNG fallback configured via `searxng_url` in config.json. Returns numbered results with title, URL, description. (5c90f86, 4cb7ce1, 405bc87)
- **web_extract**: Fetches and extracts web page content as Markdown. **5-level fallback chain**: direct HTTP → Cloudflare bypass (UA retry) → Google Cache → Wayback Machine (CDX + id_ format) → Chromium headless. **SSRF protection**: DNS resolution + IP blacklist (RFC 1918, loopback, cloud metadata). **LLM summarization**: content >5000 chars auto-summarized via provider. (5c90f86, 4cb7ce1, 4bf27e5)

### CI/CD Pipeline
- **GitHub Actions**: Two workflows — main.yml (build + lint + test on push/PR) and release.yml (cross-compile + GitHub Releases on tags v*)
- **Makefile improvements**: test target preserves exit code with pass/fail message; releases target cross-compiles all platforms + .tar.gz archives
- **359 tests passing**

### Skill System
- **SKILL.md-based discovery** — three-layer scan: embedded (skill/builtin/) → ~/.tinycode/skills/ → project .tinycode/skills/ (upward search). Later sources override earlier. (cbd6db3)
- **/skill command in TUI** — /skill lists available skills; /skill <name> loads full SKILL.md content as system message. (cbd6db3)
- **Skill index auto-injected** into system prompt at startup. Startup shows "13 tools, 2 skills loaded". (8fa8800)
- **2 builtin skills**: code-review, git-commit (as markdown files in skill/builtin/)
- **6 agents**: plan, build (primary) + explore, general (subagents) + compact, title (hidden)
- **11 new tests** across skill package and tui package — 359 tests total. (cbd6db3)
---

# Architecture

### System Overview

```
┌──────────────────────────────────────────────────────────┐
│                      TinyCode                              │
├──────────────────────┬───────────────────┬────────────────┤
│   TUI (Bubble Tea)   │   Agent Layer      │   Tool Layer   │
│                      │   (ReAct Loop)     │   (21 tools + MCP)│
│  CellGrid            │                    │                │
│  Viewport            │  Plan (primary)    │  bash          │
│  Input Area          │  Build (primary)   │  read_file     │
│  Status Bar          │  Explore (sub)     │  write_file    │
│  Command Palette     │  General (sub)     │  edit          │
│  Todo Display        │  Compact (hidden)  │  apply_patch   │
│  Reasoning Fold      │  Title (hidden)    │  search_files  │
│                      │                    │  task          │
│                      │  Registry:         │  todo          │
│                      │  Get/Set/Switch    │  memory        │
│                      │  ToolAllowedFor    │  load_skill    │
│                      │  Subagent→task     │  skill_manage  │
│                      │                    │  lsp_* (4)     │
│                      │                    │  sandbox_allow │
│                      │                    │  web_search    │
│                      │                    │  web_extract   │
└──────────────────────┴───────────────────┴────────────────┘
```

### Multi-Agent System

6 agents configured in `agent/config.go`, managed by `agent/registry.go`:

| Agent | Mode | Hidden | Tools | Steps | Purpose |
|-------|------|--------|-------|-------|---------|
| **plan** | primary | | * except {write,git,sandbox,task,skill_manage} | 20 | Read-only analysis |
| **build** | primary | | * (all) | 30 | Full access implementation |
| **explore** | subagent | | bash, read_file, search_files | 15 | Fast directory search |
| **general** | subagent | | * except {write,git,sandbox,task,skill_manage} | 20 | Parallel research |
| **compact** | primary | ✅ | (no tools) | 1 | History compression |
| **title** | primary | ✅ | (no tools) | 1 | Session title gen |

- **Primary agents**: user-switchable via Tab or /plan /build
- **Subagents**: invoked via `task` tool with independent ReAct context
- **Hidden agents**: pure LLM calls (no tools), used internally

### CellGrid Rendering Pipeline

```
Component → []CellChunk → wordWrap → Grid.AppendChunk → Grid.Render() → viewport
```

- `CellGrid` — flat array of `Cell{ Rune, Style, Width }`, auto-grows as content is added
- `CellChunk` — struct with `Text string` and `Style CellStyle` (Bold, Italic, Underline, Fg, Bg)
- `wordWrap` — splits text at width, preserves leading spaces, returns `[]CellChunk`
- `Fill` — applies `SelectionStyle` to a rectangular cell range
- `ExtractText` — returns plain text within a range, handles CJK multi-cell characters
- **Incremental rendering**: msgDirty/msgRowCount tracking. First dirty → end. ~2.3ms constant.

### Tool System

```go
type Tool struct {
    Name        string
    Description string
    Parameters  map[string]any
    Execute     func(ctx, args) (string, error)
}
```

**Line-level editing (3 tools):**
- `write_file` — create new files / full rewrites
- `edit` — search/replace with 7 fuzzy strategies + indentation correction
- `apply_patch` — V4A multi-file patch (UPDATE/ADD/DELETE)

**Sandbox (3 layers):**
1. Command blacklist (bash tool)
2. Path restriction (default: project directory only)
3. User whitelist (allow/deny/always prompt)

**Permissions:** `ToolAllowedFor(cfg, toolName)` — checked before every tool execution. Plan mode denies write/git/task/skill_manage.

### Provider Abstraction

```go
type LLMProvider interface {
    Chat(ctx, ChatRequest) (*ChatResponse, error)
    Name() string
}
```

- **DeepSeek** (default): streaming SSE support, `deepseek-v4-flash`
- **MockLLM**: step-by-step scripted responses for agent loop testing
- **ProviderRegistry**: switch providers at runtime via Tab

### Context Compression

```
History threshold: 50% of context window
  Head: system + first 2 exchanges (preserved)
  Tail: last 2 exchanges (preserved, anchored on latest user msg)
  Middle: → LLM summarization → [COMPRESSED HISTORY] system message
  Active TODO injected: [ACTIVE TODO ITEMS] after compression
```

- `/compress` command for manual trigger
- Auto-recovery: `context_length_exceeded` → lowers threshold
- Todo protection: active items re-injected in compressed output

### Data Flow

```
User Input (textarea)
  ↓
ChatMsg → agent.Run() → ReAct Loop
  │                        ├── LLM provider (streaming SSE)
  │                        ├── Tool execution (permissions checked)
  │                        └── No tool call → return final answer
  ↓
streamCh (buffered 200)
  ↓
TUI Update() → ToolCallMsg / StreamMsg / StreamDone
  ↓
TUI View() → Component.Render() → CellChunks → CellGrid
  ↓
viewport.SetContent() → terminal display
```

### Project Structure

```
agent/          Agent loop, LLM provider, context compression, registry
config/         Config loading (JSON, env, CLI flags)
lsp/            LSP client (gopls), diagnostics, Formatter, touch
session/        Session persistence (JSON files, metadata, listing, fork)
skill/          SKILL.md discovery (3-layer), Load/LoadOnce/CRUD
tool/           Tool definitions (21 tools + MCP: edit, todo, skill, LSP, web, mcp)
tui/            Bubble Tea TUI (CellGrid, components, key/mouse, cmd palette)
types/          Shared types (Message, ToolCall, StreamCallbacks)
main.go         CLI entry point with cobra
```

### Key Dependencies

| Package | Purpose |
|---------|---------|
| `bubbletea` | TUI framework, event loop |
| `bubbles/viewport` | Viewport widget |
| `bubbles/textarea` | Input textarea |
| `bubbles/spinner` | Loading spinner |
| `lipgloss` | ANSI style management |
| `go-runewidth` | CJK character width calculation |
| `go-openai` | LLM provider (OpenAI-compatible) |
| `goldmark` | Markdown parser |
| `cobra` | CLI flag handling |

---

# TODO

## Completed

- [x] **Incremental CellGrid Rendering** — msgDirty tracking, View() skips unchanged messages. Benchmark: ~2.3ms regardless of session size. (ffbbf22)
- [x] **Agent-level unit testing framework** — 13 integration tests using MockLLM step-by-step. (a3868e2)
- [x] **Multi-agent session tree** — `/fork` + `/session` branching conversations. (93f1665)
- [x] **Theming** — default + nord palettes, `/theme` command, persists to config.json. (e4bcf85, 533d167)
- [x] **Session management** — delete, export Markdown, search via CLI flags. (2236e84)
- [x] **LSP** — long-lived connection, background diagnostics, mock test framework, incremental diagnostics (SnapshotBaseline+GetNewDiagnostics), TUI error tracking (LSPDiagMsg, status bar "errors: N", /diagnostics command). (2ab4338, ace09ff, a2e3e07, 290818a)
- [x] **GitHub Actions CI/CD + Makefile improvements** — main.yml (build+lint+test), release.yml (cross-compile+release), Makefile test/releases targets. (ab07697, bddeed5)
- [x] **Skills & Subagents** — SKILL.md-based discovery + /skill command + 2 builtin skills. 3 new subagents: general (parallel research), compact (history compression), title (session naming). /explore command removed (explore kept as subagent). (cbd6db3, 8fa8800, adfa51b, c0b8ae8)
- [x] **Todo Feature — P0+P1+P2 Complete** — TodoStore + todo tool + JSON Schema (P0), TUI rendering with [x][>][ ][~] markers (P1), compression protection + housekeeping mute + session recovery (P2). 21 new tests. (2f51d06, 94db0e3, 25caefc)
- [x] **Line-Level Code Edit — edit + apply_patch** — Search/replace edit tool with 7 fuzzy strategies + indentation correction. V4A multi-file patch format. 23 new tests. (d067156, 9045176, 34d2c17)
- [x] **Title Agent & Session Titles** — title hidden agent generates conversation titles via LLM after first exchange. Applied on session save. (2deee5e)
- [x] **Edit Fuzzy Matching** — 7 fallback strategies (line-trimmed, ws-normalized, indent-flexible, escape-normalized, unicode-normalized, block-anchor). Indentation correction. 6 new tests. (34d2c17)
- [x] **Web Tools Phase 1-3** — web_search (DuckDuckGo Lite + SearXNG), web_extract (HTTP→CF→Cache→Wayback→Chromium, SSRF, LLM summary). 26 new tests. 21 tools total. (5c90f86, 4cb7ce1, 4bf27e5)
- [x] **SearXNG Config** — `searxng_url` field in config.json, wired via SetSearXNG(). (405bc87)
- [x] **MCP Client (4 Phases)** — stdio/HTTP transports, auto-discovery, agent.Tool registration, resources, SSRF security. 22 tests. 359 total. (e31c08b, cc1ba8e, 7b4fe75)

## Remaining

- [ ] **Plugin System** — JSON-RPC subprocess tools

---

<p align="center">
  <sub>Built with ❤️ and Go</sub>
</p>
