package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

// A write the kernel refused gets the grant to declare when the invocation
// failed or is still running; a refusal a successful command absorbed raises
// no card. The retry is what asks.
func TestRefusedWriteGuidanceNamesTheGrant(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	project := t.TempDir()
	boundary := confine.BoundaryOf(&confine.Confinement{Roots: []string{project}})
	if !boundary.Applied {
		t.Fatal("fixture boundary did not apply")
	}
	agents := filepath.Join(project, "AGENTS.md")
	refusals := confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{{
		Operation: "file-write-data", Target: agents, Process: "touch", Count: 1,
		Recovery: confine.RecoverWriteRoot, Grant: agents, Layer: confine.FloorAgentPolicy,
	}}}
	for _, tc := range []struct {
		name    string
		failed  []string
		running bool
		fires   bool
	}{
		{"swallowed failure", []string{"touch -f AGENTS.md"}, false, true},
		{"still running", nil, true, true},
		{"success", nil, false, false},
	} {
		mgr := newPostToolGuidanceManager(t)
		obs := confine.StampRefusal("command", "sess-route", boundary, confine.RefusalContext{
			FailedStages: tc.failed, Running: tc.running, Refusals: refusals,
		}).Observation
		out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "sess-route"}, "command",
			map[string]any{"command": "touch -f AGENTS.md || true"}, `{"exit_code":0}`, 1, guidance.ToolResultFacts{Confine: obs})
		fired := strings.Contains(out, "Code: "+isolation.CodeTryWriteRoot)
		if fired != tc.fires {
			t.Fatalf("%s: guidance fired=%v, want %v:\n%s", tc.name, fired, tc.fires, out)
		}
		if !tc.fires {
			continue
		}
		if !strings.Contains(out, agents) || !facts.HasCode(isolation.CodeTryWriteRoot) {
			t.Fatalf("%s: guidance did not name the grant, or state its code:\n%s\n%#v", tc.name, out, facts)
		}
	}
}

// An operation no capability admits routes to host execution, and a running
// job is told to stop before the retry.
func TestUnsandboxedRefusalRoutesToHostExecution(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	boundary := confine.BoundaryOf(&confine.Confinement{Roots: []string{t.TempDir()}})
	socket := "/Users/person/.colima/_lima/_networks/user-v2/user-v2_fd.sock"
	obs := confine.StampRefusal("command", "sess-host", boundary, confine.RefusalContext{
		Running: true,
		Refusals: confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{{
			Operation: "network-bind", Target: socket, Process: "limactl", Count: 3,
			Recovery: confine.RecoverHostExecution,
		}}},
	}).Observation
	mgr := newPostToolGuidanceManager(t)
	out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "sess-host"}, "command",
		map[string]any{"command": "colima start"}, `{"running":true}`, 1, guidance.ToolResultFacts{Confine: obs})
	for _, want := range []string{"Code: " + isolation.CodeTryHostExecution, socket, "command_stop", "capability_request.host_execution"} {
		if !strings.Contains(out, want) {
			t.Fatalf("guidance lacks %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(isolation.CodeTryHostExecution) {
		t.Fatalf("OAR must state the code on facts: %#v", facts)
	}
}

// A branch switch the sandbox kept out of an instruction file routes to the
// native tools that finish it under review.
func TestWorktreeBehindIndexRoutesToNativeTools(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	docs := filepath.Join(dir, "docs")
	testutil.FailErr(t, "create docs", os.MkdirAll(docs, 0o755))
	testutil.FailErr(t, "write main", os.WriteFile(filepath.Join(docs, "AGENTS.md"), []byte("main\n"), 0o644))
	gittest.CommitAll(t, dir, "Initial")
	gittest.Run(t, dir, "branch", "-M", "main")
	gittest.Run(t, dir, "checkout", "-q", "-b", "other")
	testutil.FailErr(t, "write other", os.WriteFile(filepath.Join(docs, "AGENTS.md"), []byte("other\n"), 0o644))
	gittest.CommitAll(t, dir, "Other")
	gittest.Run(t, dir, "checkout", "-q", "main")

	snapshot := indexwatch.Take(dir)
	testutil.FailErr(t, "lock docs", os.Chmod(docs, 0o555))
	t.Cleanup(func() { _ = os.Chmod(docs, 0o755) })
	gittest.Run(t, dir, "checkout", "-q", "other")

	mgr := newPostToolGuidanceManager(t)
	out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "sess-worktree"}, "command",
		map[string]any{"command": "git checkout other"}, `{"exit_code":0}`, 1, guidance.ToolResultFacts{
			Confine:    confine.Observation{Applied: true},
			IndexWatch: snapshot,
		})
	if !strings.Contains(out, "Code: "+isolation.CodeWorktreeBehindIndex) || !strings.Contains(out, "docs/AGENTS.md") {
		t.Fatalf("worktree divergence carried no route:\n%s", out)
	}
	if !facts.HasCode(isolation.CodeWorktreeBehindIndex) {
		t.Fatalf("OAR must state the code on facts: %#v", facts)
	}
	if snapshot.Active() {
		t.Fatal("post-tool guidance did not release the index capture")
	}
}
