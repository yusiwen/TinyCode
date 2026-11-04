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
// tree while it runs, and the session's tree stays clean until the merge.
func TestWritingTaskGetsItsOwnWorktree(t *testing.T) {
	repo := worktreeRepo(t)
	deps := writeDeps(t, writingProvider("made.txt"), true, "write_file")

	// The isolation half, without a model in the way: the task's tree gets the
	// write, the repository does not, and only the apply brings it over.
	cfg := deps.GetAgentConfig("general")
	tree, note := prepareSubagentWorktree(types.WithToolCallScope(context.Background(),
		types.ToolCallScope{Run: 1}), deps, cfg, "general#1")
	if tree.path == "" {
		t.Fatalf("no worktree was created: %q", note)
	}
	t.Cleanup(func() { _ = RemoveWorktree(tree.toplevel, tree.path) })
	if _, err := WriteFile().Execute(types.WithSandboxPolicy(context.Background(), policyWithRoot(context.Background(), tree.path)), map[string]any{
		"path": "made.txt", "content": "from the task\n",
	}); err != nil {
		t.Fatalf("write in the task's tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "made.txt")); err == nil {
		t.Fatal("the write landed in the session's tree before the merge")
	}
	apply := applyWorktree(tree.toplevel, tree.path, "general#1", 1)
	if apply.Err != nil || len(apply.Merged) != 1 {
		t.Fatalf("apply = %+v", apply)
	}
	if _, err := os.Stat(filepath.Join(repo, "made.txt")); err != nil {
		t.Fatalf("the merge did not land: %v", err)
	}
}

// TestWritingTaskMergesAndRemovesItsTree: the tool's path — a writing task's
// work is in the repository afterwards and its tree is gone.
func TestWritingTaskMergesAndRemovesItsTree(t *testing.T) {
	repo := worktreeRepo(t)
	tool := TaskTool(writeDeps(t, writingProvider("merged.txt"), true, "write_file"))

	out, err := tool.Execute(context.Background(), map[string]any{"agent": "general", "goal": "write it"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if !strings.Contains(out, "[merged] 1 file(s)") || !strings.Contains(out, "merged.txt") {
		t.Fatalf("result = %q", out)
	}
	data, err := os.ReadFile(filepath.Join(repo, "merged.txt"))
	if err != nil || !strings.Contains(string(data), "from the task") {
		t.Fatalf("the merged file = %q, %v", data, err)
	}
	if entries, _ := filepath.Glob(filepath.Join(filepath.Dir(repo), ".*-worktrees", "*")); len(entries) != 0 {
		t.Fatalf("the worktree was not removed: %v", entries)
	}
}

// TestConflictKeepsTheSecondTasksTree: two tasks in one run writing the same file
// must not silently overwrite each other.
func TestConflictKeepsTheSecondTasksTree(t *testing.T) {
	repo := worktreeRepo(t)
	// One parent run for both calls: that is what the conflict registry keys on.
	ctx := types.WithToolCallScope(context.Background(), types.ToolCallScope{Run: 42})
	first := TaskTool(writeDeps(t, writingProvider("shared.txt"), true, "write_file"))
	if _, err := first.Execute(ctx, map[string]any{"agent": "general", "goal": "write it"}); err != nil {
		t.Fatalf("first task: %v", err)
	}
	second := TaskTool(writeDeps(t, writingProvider("shared.txt"), true, "write_file"))
	out, err := second.Execute(ctx, map[string]any{"agent": "general", "goal": "write it too"})
	if err != nil {
		t.Fatalf("second task: %v", err)
	}
	if !strings.Contains(out, "[conflict]") || !strings.Contains(out, "shared.txt") {
		t.Fatalf("result = %q", out)
	}
	if !strings.Contains(out, "was not applied") {
		t.Fatalf("result does not say the work was kept: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "shared.txt")); err != nil {
		t.Fatalf("the first task's file is missing: %v", err)
	}
}

// TestMergeLeavesUnrelatedChangesAlone: the apply touches the task's paths only.
func TestMergeLeavesUnrelatedChangesAlone(t *testing.T) {
	repo := worktreeRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", "cd "+repo+" && git add -A && git commit -m unrelated").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps9 := writeDeps(t, writingProvider("x"), true, "write_file")
	tree, _ := prepareSubagentWorktree(context.Background(), deps9, deps9.GetAgentConfig("general"), "general#9")
	if tree.path == "" {
		t.Fatal("no worktree created")
	}
	t.Cleanup(func() { _ = RemoveWorktree(tree.toplevel, tree.path) })
	if _, err := WriteFile().Execute(types.WithSandboxPolicy(context.Background(), policyWithRoot(context.Background(), tree.path)), map[string]any{
		"path": "task.txt", "content": "task\n",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if apply := applyWorktree(tree.toplevel, tree.path, "general#9", 9); apply.Err != nil {
		t.Fatalf("apply: %v", apply.Err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "unrelated.txt"))
	if err != nil || string(data) != "local edit\n" {
		t.Fatalf("the unrelated change was touched: %q, %v", data, err)
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

// TestBackgroundTaskGetsItsOwnWorktree: the same isolation for a background task,
// whose context is gone by the time it runs — the policy and the run number ride
// along from the call that started it.
func TestBackgroundTaskGetsItsOwnWorktree(t *testing.T) {
	repo := worktreeRepo(t)
	mgr := NewBackgroundTaskManager()
	deps := writeDeps(t, writingProvider("bg.txt"), true, "write_file")
	ctx := types.WithToolCallScope(context.Background(), types.ToolCallScope{Run: 77})

	id := mgr.Start(ctx, deps, "general", "write it in the background")
	result, err := mgr.CollectContext(context.Background(), id)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !strings.Contains(result, "[merged] 1 file(s)") {
		t.Fatalf("result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(repo, "bg.txt")); err != nil {
		t.Fatalf("the background task's work was not merged: %v", err)
	}
}
