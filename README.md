<p align="center">
  <img src="https://img.shields.io/badge/TinyCode-1.0.0-%23FFD700?style=for-the-badge" alt="TinyCode"/>
  <img src="https://img.shields.io/badge/Go-1.24-%2300ADD8?style=for-the-badge&logo=go" alt="Go 1.24"/>
  <img src="https://img.shields.io/badge/License-MIT-%23green?style=for-the-badge" alt="MIT License"/>
</p>

<p align="center">
  <b>AI Coding Agent with CellGrid TUI &mdash; markdown-native rendering, reasoning folding, tool call display, session persistence</b>
</p>

<p align="center">
  <img src="https://img.shields.io/github/last-commit/yusiwen/tinycode?style=flat-square"/>
  <img src="https://img.shields.io/github/repo-size/yusiwen/tinycode?style=flat-square"/>
  <img src="https://img.shields.io/badge/tests-208-%23success?style=flat-square"/>
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
- Input history: Up/Down arrows to recall previous inputs, Esc to exit browse mode
- Status bar shows `hist 3/5` during history browsing

### TUI Features
- **Reasoning folding**: click `[+]`/`[-]` markers to expand/collapse LLM reasoning blocks
- **Tool call display**: `→ Calling tools:` with bullet list and `⏳ Running...` indicator during tool execution
- **Character-level selection**: drag-select any range of viewport text, Ctrl+C copies rendered text (not raw markdown)
- **[Copy] buttons**: click to copy assistant response to clipboard
- **Response label**: gold bold `Response:` with blank line before for visual separation
- **Auto-scroll**: viewport follows streaming output, pauses when user scrolls up
- **Status bar**: mode icon, model name, provider, token/tool/msg counts, session duration

### Agent Loop
- ReAct loop with tool calling support (bash, read_file, search_files, git, LSP tools)
- Streaming reasoning + text deltas
- Tool call lifecycle displayed in real-time
- Multi-turn history compression: Hermes-style head/tail/middle summarization
- 1M token context (DeepSeek V4 Flash) with automatic threshold lowering on `context_length_exceeded` errors

---

# Architecture

### CellGrid Rendering Pipeline

```
Component → []CellChunk → CellGrid.Append/AppendInline → CellGrid.Render() → viewport
```

- `CellGrid` — flat array of `Cell{ Rune, Style, Width }`, auto-grows as content is added
- `CellChunk` — struct with `Text string` and `Style CellStyle` (Bold, Italic, Underline, Fg, Bg)
- `wordWrap` — splits text at width, preserves leading spaces (indent), returns `[]CellChunk`
- `Fill` — applies `SelectionStyle` to a rectangular cell range
- `ExtractText` — returns plain text within a range, handles CJK multi-cell characters
- `Render` — produces ANSI-formatted string via `styleToLipgloss()` (cached with `sync.RWMutex`)
- ~8 distinct CellStyles cached after first render

### Project Structure

```
agent/          Agent loop, LLM provider abstraction, context compression
config/         Config loading (JSON, env, CLI flags)
session/        Session persistence (JSON files, metadata, listing)
tool/           Tool definitions (bash, filesystem, sandbox)
tui/            Bubble Tea TUI with CellGrid, components, key/mouse handling
types/          Shared types (Message, ToolCall, StreamCallbacks)
main.go         CLI entry point with cobra
```

### Data Flow

```
User Input (textarea)
  ↓
ChatMsg → agent.Run() → Agent Loop
  │                        ├── LLM provider (streaming)
  │                        ├── Tool execution
  │                        └── Callbacks (StreamCallbacks)
  ↓
streamCh (buffered 200)
  ↓
TUI Update()  →  ToolCallMsg / StreamMsg / StreamDone
  ↓
TUI View()    →  Component.Render() → CellChunks → CellGrid
  ↓
viewport.SetContent() → terminal display
```

### Context Compression

```
History threshold (50% of context window):
  Head: system + first 2 exchanges (protected)
  Tail: last 2 exchanges (protected, anchored on latest user msg)
  Middle: → LLM summarization → [COMPRESSED HISTORY] system message

Error recovery:
  ParseContextLimitFromError() extracts limit from API error message
  HandleContextError() lowers EffectiveContextLength + CompressionThreshold
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

- [ ] **Agent-level unit testing framework** — mock LLM provider for deterministic test runs
- [ ] **Multi-agent session tree** — branching conversations with `/continue`
- [ ] **Line-level code edit** — apply LLM suggestions as diffs to project files
- [ ] **File watching** — auto-refresh on external file changes
- [ ] **LSP integration Phase 2** — inline diagnostics, completion, rename
- [ ] **Auto-formatting** — `gofmt` / `prettier` integration for code blocks
- [ ] **Session import/export** — JSON / Markdown export of conversation history
- [ ] **Theming** — customizable color palette via config file

---

<p align="center">
  <sub>Built with ❤️ and Go</sub>
</p>
