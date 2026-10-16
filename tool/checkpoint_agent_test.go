package tool

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// TestTheAgentLoopStampsACheckpointScope is the wiring gate for S1: the tool
// tests below the agent stamp the scope themselves, so none of them would notice
// the loop dropping it. This one drives the real loop — a scripted provider that
// asks for `write_file` — and asserts that a manifest exists, carries the
// attribution the loop alone can supply, and restores the file.
func TestTheAgentLoopStampsACheckpointScope(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	target := filepath.Join(project, "written.txt")

	llm := agent.NewMockLLM([]agent.MockStep{
		{ToolCalls: []types.ToolCall{{
			ID:        "call_1",
			Name:      "write_file",
			Arguments: `{"path":"` + target + `","content":"from the loop\n"}`,
		}}},
		{Content: "done"},
	})
	ag := agent.MockAgent(llm, nil)
	wf := WriteFile()
	ag.AddTool(agent.Tool{Name: wf.Name, Description: wf.Description, Parameters: wf.Parameters, Execute: wf.Execute})

	if _, err := ag.Run(context.Background(), "write the file"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the tool did not write the file: %v", err)
	}

	calls, err := ListCalls(0)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("the loop recorded %d calls, want 1: %+v", len(calls), calls)
	}
	if calls[0].Tool != "write_file" {
		t.Errorf("recorded tool = %q, want write_file", calls[0].Tool)
	}
	if calls[0].Run == 0 {
		t.Error("the run counter must name the run, or two runs in one process share step numbers")
	}
	if len(calls[0].Entries) != 1 || calls[0].Entries[0].Existed {
		t.Errorf("entries = %+v, want one record that the file did not exist", calls[0].Entries)
	}

	if _, err := RestoreCall(context.Background(), calls[0]); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the restore must remove the file the call created: %v", err)
	}
}
