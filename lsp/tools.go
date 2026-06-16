package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusiwen/tinycode/agent"
)

// ToolType identifies which LSP operation to perform.
type ToolType string

const (
	ToolGoToDefinition ToolType = "lsp_definition"
	ToolFindReferences ToolType = "lsp_references"
	ToolHover          ToolType = "lsp_hover"
	ToolDocumentSymbols ToolType = "lsp_symbols"
)

// ToolFactory creates an agent.Tool for the given LSP operation.
func ToolFactory(tt ToolType) agent.Tool {
	desc := map[ToolType]string{
		ToolGoToDefinition: "Find the definition of a symbol at a given file:line:character. Pass file_path, line, character.",
		ToolFindReferences: "Find all references to a symbol at a given file:line:character. Pass file_path, line, character.",
		ToolHover:          "Get type information and documentation for a symbol at a given file:line:character. Pass file_path, line, character.",
		ToolDocumentSymbols: "List all symbols (functions, types, variables) defined in a file. Pass file_path.",
	}

	return agent.Tool{
		Name:        string(tt),
		Description: desc[tt],
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the source file",
				},
				"line": map[string]any{
					"type":        "integer",
					"description": "Line number (0-indexed)",
				},
				"character": map[string]any{
					"type":        "integer",
					"description": "Character offset in the line (0-indexed)",
				},
			},
			"required": []string{"file_path"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			filePath, _ := args["file_path"].(string)
			if filePath == "" {
				return "", fmt.Errorf("file_path is required")
			}

			// Resolve absolute path
			absPath, err := filepath.Abs(filePath)
			if err != nil {
				return "", fmt.Errorf("resolve path: %w", err)
			}

			// Detect project root (walk up to find go.mod)
			rootDir := findProjectRoot(absPath)
			if rootDir == "" {
				rootDir = filepath.Dir(absPath)
			}

			// Prefer persistent LSP connection if available
			fileURI := "file://" + absPath
			line := int(args["line"].(float64))
			character := int(args["character"].(float64))
			if IsAvailable() {
				return executeViaPersistent(ctx, tt, fileURI, line, character)
			}

			// Fallback: start per-call LSP server
			lang := DetectLanguage(rootDir)
			if lang == "" {
				// Fallback: infer from file extension
				ext := filepath.Ext(absPath)
				switch ext {
				case ".go":     lang = "go"
				case ".py":     lang = "python"
				case ".ts", ".tsx", ".js", ".jsx": lang = "typescript"
				case ".rs":     lang = "rust"
				case ".java":   lang = "java"
				case ".c", ".cpp", ".h", ".hpp": lang = "cpp"
				}
			}

			cfg := FindConfig(lang)
			if cfg == nil {
				return "", fmt.Errorf("no LSP server configured for %s language", lang)
			}

			// Start LSP server
			srv, err := Start(ctx, cfg.Command, cfg.Args...)
			if err != nil {
				return "", fmt.Errorf("start LSP: %w", err)
			}
			defer srv.Close()

			// Initialize
			if err := srv.Client.Initialize(fileURI); err != nil {
				return "", fmt.Errorf("initialize LSP: %w", err)
			}

			// Execute the requested operation
			line = int(args["line"].(float64))
			character = int(args["character"].(float64))

			switch tt {
			case ToolGoToDefinition:
				loc, err := srv.Client.GoToDefinition(fileURI, line, character)
				if err != nil {
					return "", err
				}
				if loc == nil {
					return "No definition found.", nil
				}
				return fmt.Sprintf("Definition at %s:%d:%d", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character+1), nil

			case ToolFindReferences:
				locs, err := srv.Client.FindReferences(fileURI, line, character)
				if err != nil {
					return "", err
				}
				if len(locs) == 0 {
					return "No references found.", nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d references:\n", len(locs)))
				for _, loc := range locs {
					sb.WriteString(fmt.Sprintf("  %s:%d:%d\n", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
				}
				return sb.String(), nil

			case ToolHover:
				hover, err := srv.Client.Hover(fileURI, line, character)
				if err != nil {
					return "", err
				}
				if hover == nil || hover.Contents.Value == "" {
					return "No hover information available.", nil
				}
				return hover.Contents.Value, nil

			case ToolDocumentSymbols:
				syms, err := srv.Client.DocumentSymbols(fileURI)
				if err != nil {
					return "", err
				}
				if len(syms) == 0 {
					return "No symbols found.", nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Symbols in %s:\n", absPath))
				for _, sym := range syms {
					sb.WriteString(fmt.Sprintf("  %s (%s:%d)\n",
						sym.Name, sym.Location.URI, sym.Location.Range.Start.Line+1))
				}
				return sb.String(), nil
			}

			return "", fmt.Errorf("unknown LSP tool type: %s", tt)
		},
	}
}

// executeViaPersistent runs an LSP operation on the persistent connection.
func executeViaPersistent(ctx context.Context, tt ToolType, fileURI string, line, character int) (string, error) {
	mu.Lock()
	c := client
	mu.Unlock()
	if c == nil {
		return "", fmt.Errorf("LSP client not available")
	}

	switch tt {
	case ToolGoToDefinition:
		loc, err := c.GoToDefinition(fileURI, line, character)
		if err != nil {
			return "", err
		}
		if loc == nil {
			return "No definition found.", nil
		}
		return fmt.Sprintf("Definition at %s:%d:%d", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character+1), nil

	case ToolFindReferences:
		locs, err := c.FindReferences(fileURI, line, character)
		if err != nil {
			return "", err
		}
		if len(locs) == 0 {
			return "No references found.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d references:\n", len(locs)))
		for _, loc := range locs {
			sb.WriteString(fmt.Sprintf("  %s:%d:%d\n", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		return sb.String(), nil

	case ToolHover:
		hover, err := c.Hover(fileURI, line, character)
		if err != nil {
			return "", err
		}
		if hover == nil || hover.Contents.Value == "" {
			return "No hover information available.", nil
		}
		return hover.Contents.Value, nil

	case ToolDocumentSymbols:
		syms, err := c.DocumentSymbols(fileURI)
		if err != nil {
			return "", err
		}
		if len(syms) == 0 {
			return "No symbols found.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Symbols in %s:\n", fileURI))
		for _, sym := range syms {
			sb.WriteString(fmt.Sprintf("  %s (%s:%d)\n",
				sym.Name, sym.Location.URI, sym.Location.Range.Start.Line+1))
		}
		return sb.String(), nil
	}

	return "", fmt.Errorf("unknown LSP tool type: %s", tt)
}
func findProjectRoot(filePath string) string {
	dir := filepath.Dir(filePath)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
