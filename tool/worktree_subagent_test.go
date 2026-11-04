package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// worktreeRepo makes a git repository with one commit and points the sandbox at
// it, and returns its path.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	_, _ = checkpointTestSetup(t)
	repo := t.TempDir()
	for _, cmd := range []string{"git init", "git config user.email t@t", "git config user.name t"} {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", "git add -A && git commit -m init")
	c.Dir = repo
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	DefaultSandbox.ProjectRoot = repo
	// A sub-agent's relative paths resolve against the process directory when no
	// run policy redirects them, so the test runs where the session would.
	t.Chdir(repo)
	return repo
}

// writingProvider answers with one write_file call and then a final answer. The
// path is relative on purpose: a delegated task's paths are relative to its own
// tree.
func writingProvider(file string) *agent.MockProvider {
	step := 0
	return &agent.MockProvider{ChatFunc: func(_ context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
		step++
		if step == 1 {
			return &types.ChatResponse{ToolCalls: []types.ToolCall{{
				ID: "c1", Name: "write_file", Arguments: `{"path":"` + file + `","content":"from the task\n"}`,
			}}}, nil
		}
		return &types.ChatResponse{Content: "done"}, nil
	}}
}

// writeDeps assembles the task tool for a sub-agent that may write.
func writeDeps(t *testing.T, provider agent.LLMProvider, worktrees bool, allowed ...string) *TaskToolDeps {
	t.Helper()
	writeFile := WriteFile()
	return &TaskToolDeps{
		AllTools: []agent.Tool{{
			Name: writeFile.Name, Description: writeFile.Description,
			Parameters: writeFile.Parameters, Execute: writeFile.Execute,
		}},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{Name: name, AllowedTools: allowed, MaxSteps: 3}
		},
		ResolveProvider: func(string) (agent.LLMProvider, error) { return provider, nil },
		Worktrees:       worktrees,
	}
}

// TestWritingTaskGetsItsOwnWorktree is S2's core: the write lands in the task's
// tree and the session's tree stays clean.
func TestWritingTaskGetsItsOwnWorktree(t *testing.T) {
	repo := worktreeRepo(t)
	tool := TaskTool(writeDeps(t, writingProvider("made.txt"), true, "write_file"))

	out, err := tool.Execute(context.Background(), map[string]any{"agent": "general", "goal": "write it"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if !strings.Contains(out, "[worktree] this task worked in its own tree") {
		t.Fatalf("result does not name the worktree: %q", out)
	}
	path := worktreePathFrom(out)
	if path == "" {
		t.Fatalf("no worktree path in %q", out)
	}
	t.Cleanup(func() { _ = RemoveWorktree(repo, path) })

	if _, err := os.Stat(filepath.Join(path, "made.txt")); err != nil {
		t.Fatalf("the write is not in the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "made.txt")); err == nil {
		t.Fatal("the write also landed in the session's tree")
	}
}

// TestReadOnlyTaskGetsNoWorktree: an agent without write tools costs no checkout.
func TestReadOnlyTaskGetsNoWorktree(t *testing.T) {
	worktreeRepo(t)
	tool := TaskTool(writeDeps(t, writingProvider("ignored.txt"), true, "read_file"))

	out, err := tool.Execute(context.Background(), map[string]any{"agent": "explore", "goal": "look"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if strings.Contains(out, "[worktree]") {
		t.Fatalf("a read-only task created a worktree: %q", out)
	}
}

// TestWorktreesOffSharesTheTree: the default keeps today's behaviour.
func TestWorktreesOffSharesTheTree(t *testing.T) {
	repo := worktreeRepo(t)
	tool := TaskTool(writeDeps(t, writingProvider("shared.txt"), false, "write_file"))

	out, err := tool.Execute(context.Background(), map[string]any{"agent": "general", "goal": "write it"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if strings.Contains(out, "[worktree]") {
		t.Fatalf("a worktree was created with isolation off: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "shared.txt")); err != nil {
		t.Fatalf("the write did not land in the shared tree: %v", err)
	}
}

// TestNotAGitRepositorySaysSo: an unversioned project falls back and names why.
func TestNotAGitRepositorySaysSo(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	t.Chdir(project)
	tool := TaskTool(writeDeps(t, writingProvider("plain.txt"), true, "write_file"))

	out, err := tool.Execute(context.Background(), map[string]any{"agent": "general", "goal": "write it"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if !strings.Contains(out, "not a git repository") {
		t.Fatalf("result = %q", out)
	}
	if _, err := os.Stat(filepath.Join(project, "plain.txt")); err != nil {
		t.Fatalf("the write did not land in the shared tree: %v", err)
	}
}

// worktreePathFrom pulls the path out of the note the task appends.
func worktreePathFrom(out string) string {
	const marker = "its own tree: "
	i := strings.Index(out, marker)
	if i < 0 {
		return ""
	}
	rest := out[i+len(marker):]
	if j := strings.Index(rest, " —"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}
