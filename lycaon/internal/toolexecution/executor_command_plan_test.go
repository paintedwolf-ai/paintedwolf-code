package toolexecution_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingApprovalGate lets every action through and keeps what it reviewed.
type recordingApprovalGate struct {
	countingApprovalGate
	mu      sync.Mutex
	actions []hitl.ProposedAction
}

func (g *recordingApprovalGate) Evaluate(_ context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actions = append(g.actions, action)
	return &hitl.ApprovalResult{}, nil
}

// last returns the command review of the most recent invocation.
func (g *recordingApprovalGate) last(t *testing.T) hitl.ProposedAction {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, action := range g.actions {
		if action.Tool == "command" {
			return action
		}
	}
	t.Fatal("the approval gate reviewed no command")
	return hitl.ProposedAction{}
}

func (g *recordingApprovalGate) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actions = nil
}

type commandPlanFixture struct {
	root     string
	gate     *recordingApprovalGate
	executor *toolexecution.Executor
	tc       tools.ToolContext
}

func newCommandPlanFixture(t *testing.T) commandPlanFixture {
	t.Helper()
	root := t.TempDir()
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	command := &native.CommandTool{
		Runner: hostcmd.NewRunner(), Boundary: boundary,
		Background: bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{}),
	}
	testutil.FailErr(t, "register command", registry.Register("command", command.Run))
	gate := &recordingApprovalGate{}
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), registry, "implement")
	return commandPlanFixture{root: root, gate: gate, executor: executor, tc: tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root",
		ProjectID: "project", SourceWorkspaceKind: api.SourceWorkspaceKindProject, SessionID: "chat", ToolCallID: "call", Agent: "implement",
	}}
}

func (f commandPlanFixture) run(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	f.gate.reset()
	out, err := f.executor.Invoke(t.Context(), "command", args, f.tc)
	testutil.FailErr(t, "invoke "+args["command"].(string), err)
	var result map[string]any
	testutil.FailErr(t, "decode result", json.Unmarshal([]byte(out), &result))
	if result["ok"] != true {
		t.Fatalf("%s did not succeed: %#v", args["command"], result)
	}
	return result
}

func (f commandPlanFixture) read(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	testutil.FailErr(t, "read "+rel, err)
	return string(body)
}

func TestInlineRedirectionRunsThroughTheExecutor(t *testing.T) {
	f := newCommandPlanFixture(t)
	testutil.FailErr(t, "seed input", os.WriteFile(filepath.Join(f.root, "in.txt"), []byte("b\na\n"), 0o644))
	testutil.FailErr(t, "seed cwd", os.MkdirAll(filepath.Join(f.root, "pkg"), 0o755))

	args := map[string]any{"command": "echo built > out.log"}
	f.run(t, args)
	if got := f.read(t, "out.log"); got != "built\n" {
		t.Fatalf("out.log = %q", got)
	}
	if _, mutated := args["stdout_to"]; mutated || len(args) != 1 {
		t.Fatalf("the executor rewrote the caller's arguments: %#v", args)
	}
	if files := f.gate.last(t).Files; !slices.Contains(files, "out.log") {
		t.Fatalf("approval did not see the redirect target: %q", files)
	}

	f.run(t, map[string]any{"command": `sh -c "echo broken >&2" 2> err.log`})
	if got := f.read(t, "err.log"); got != "broken\n" {
		t.Fatalf("err.log = %q", got)
	}

	result := f.run(t, map[string]any{"command": "sort < ../in.txt > sorted.txt && cat sorted.txt", "cwd": "pkg"})
	if got := f.read(t, "pkg/sorted.txt"); got != "a\nb\n" {
		t.Fatalf("pkg/sorted.txt = %q", got)
	}
	if tail, _ := result["tail"].(string); !strings.Contains(tail, "a\nb") {
		t.Fatalf("the second group did not read the first group's output: %#v", result)
	}
	files := f.gate.last(t).Files
	if !slices.Contains(files, "pkg/sorted.txt") || !slices.Contains(files, "pkg/../in.txt") {
		t.Fatalf("approval did not see the cwd-relative stream files: %q", files)
	}
}

func TestApprovalReviewsTheExpandedArgv(t *testing.T) {
	f := newCommandPlanFixture(t)
	for _, name := range []string{"a.log", "b.log", ".hidden.log"} {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(f.root, name), []byte(name), 0o644))
	}
	args := map[string]any{"command": "cat *.log > joined.txt"}
	f.run(t, args)
	reviewed, _ := f.gate.last(t).Args["command"].(string)
	if reviewed != "cat a.log b.log > joined.txt" {
		t.Fatalf("approval reviewed %q, want the expanded argv", reviewed)
	}
	if got := f.read(t, "joined.txt"); got != "a.logb.log" {
		t.Fatalf("joined.txt = %q, want the expanded files only", got)
	}
	if args["command"] != "cat *.log > joined.txt" {
		t.Fatalf("expansion rewrote the caller's arguments: %#v", args)
	}

	f.run(t, map[string]any{"command": "echo '*.log'"})
	if reviewed, _ := f.gate.last(t).Args["command"].(string); reviewed != "echo '*.log'" {
		t.Fatalf("a quoted pattern was expanded: %q", reviewed)
	}
}
