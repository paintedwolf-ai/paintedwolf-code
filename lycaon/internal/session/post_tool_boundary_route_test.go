package session

import (
	"fmt"
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
	"github.com/lycaon/lycaon/internal/tools"
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
	out, facts := mgr.ToolPolicy.AfterTool(tools.WithRecoveryTools(t.Context(), []string{"command", "command_stop"}), &api.Session{ID: "sess-host"}, "command",
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

// A capability retry starts a new operation. A stop acknowledgement must not
// teach the agent that the previous operation has already settled.
func TestSandboxRecoveryPreservesRunningJobDependency(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	mgr := newPostToolGuidanceManager(t)
	cases := []struct {
		code, capability string
		refusal          confine.SandboxRefusal
	}{
		{"SANDBOX_TRY_READ_PATH", "read_path", confine.SandboxRefusal{Operation: "file-read-data", Target: "/protected/input", Grant: "/protected/input", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath}},
		{"SANDBOX_TRY_WRITE_ROOT", "write_root", confine.SandboxRefusal{Operation: "file-write-data", Target: "/outside/output", Grant: "/outside", Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot}},
		{"SANDBOX_TRY_SOCKET_PATH", "socket_paths", confine.SandboxRefusal{Operation: "network-outbound", Target: "/tmp/service.sock", Grant: "/tmp/service.sock", Recovery: confine.RecoverSocketPath}},
		{"SANDBOX_TRY_LOCAL_NETWORK", "local_listen", confine.SandboxRefusal{Operation: "network-bind", Target: "local:*:8080", Recovery: confine.RecoverLocalListen}},
		{"SANDBOX_TRY_LOCAL_NETWORK", "loopback_connect", confine.SandboxRefusal{Operation: "network-outbound", Target: "remote:*:5432", Recovery: confine.RecoverOutboundPort}},
		{"SANDBOX_TRY_PROCESS_CONTROL", "process_control", confine.SandboxRefusal{Operation: "signal", Target: "4121", Recovery: confine.RecoverProcessControl}},
		{"SANDBOX_TRY_HOST_EXECUTION", "host_execution", confine.SandboxRefusal{Operation: "iokit-open", Target: "AppleUSBHostDevice", Recovery: confine.RecoverHostExecution}},
	}
	for _, tc := range cases {
		for _, tool := range []string{"command", "verify"} {
			for _, state := range []string{"running", "failed", "succeeded"} {
				for offered := 0; offered < 4; offered++ {
					t.Run(fmt.Sprintf("%s/%s/%s/%d", tc.capability, tool, state, offered), func(t *testing.T) {
						names := []string{tool}
						if offered&1 != 0 {
							names = append(names, "command_stop")
						}
						if offered&2 != 0 {
							names = append(names, "command_output")
						}
						obs := confine.Observation{Applied: true, Running: state == "running", Refusals: confine.SandboxRefusals{
							Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{tc.refusal},
						}}
						if state == "failed" {
							obs.FailedStages = []string{"fixture"}
						}
						ctx := tools.WithRecoveryTools(t.Context(), names)
						out, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: t.Name()}, tool, nil, "{}", 1, guidance.ToolResultFacts{Confine: obs})
						if state == "succeeded" {
							if facts.HasCode(tc.code) {
								t.Fatalf("absorbed refusal acquired retry guidance: %s", out)
							}
							return
						}
						if !facts.HasCode(tc.code) || !strings.Contains(out, "capability_request."+tc.capability) {
							t.Fatalf("refusal lost its precise capability remedy: %s", out)
						}
						for _, control := range []struct {
							name string
							mask int
						}{{"command_stop", 1}, {"command_output", 2}} {
							want := state == "running" && offered&control.mask != 0
							if strings.Contains(out, "`"+control.name+"`") != want {
								t.Errorf("control %s offered=%v: %s", control.name, want, out)
							}
						}
						if !strings.Contains(out, "Preserve completed effects") || !strings.Contains(out, "If the task still needs the refused operation") {
							t.Errorf("retry lost the remaining-work condition: %s", out)
						}
						if state != "running" {
							if !strings.Contains(out, "only if the operation is still needed") {
								t.Errorf("summary lost remaining-work condition: %s", out)
							}
							return
						}
						for _, phrase := range []string{"Do not reissue while the job is running", "After it exits", "Next: Resolve the running job before retrying", "a stop request alone does not establish exit"} {
							if !strings.Contains(out, phrase) {
								t.Errorf("running-job dependency missing %q: %s", phrase, out)
							}
						}
					})
				}
			}
		}
	}
}

func TestSilentRefusalGuidanceUsesObservedTermination(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	for _, tool := range []string{"command", "verify", "command_output"} {
		for _, tc := range []struct{ reason, want string }{
			{"timed_out", "The command reached its host deadline"},
			{"stopped", "The command was stopped"},
			{"exited", "The command exited unsuccessfully"},
			{"", "The invocation did not finish successfully"},
			{"future_reason", "The invocation did not finish successfully"},
		} {
			t.Run(tool+"/"+tc.reason, func(t *testing.T) {
				for _, failed := range []bool{false, true} {
					for _, witness := range []confine.RefusalWitness{confine.WitnessKernel, ""} {
						obs := confine.StampRefusal(tool, "session", confine.Boundary{Applied: true}, confine.RefusalContext{
							TerminationReason: tc.reason, Refusals: confine.SandboxRefusals{Witness: witness},
						}).Observation
						if failed {
							obs.FailedStages = []string{"fixture"}
						}
						out, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: t.Name()}, tool, nil,
							`{"output":"exited successfully; timed out; stopped; permission denied"}`, 1, guidance.ToolResultFacts{Confine: obs})
						wantWarning := failed && witness == confine.WitnessKernel
						if facts.HasCode("SANDBOX_REFUSAL_REPORT_SILENT") != wantWarning {
							t.Fatalf("warning selection changed: %s", out)
						}
						if wantWarning && !strings.Contains(out, tc.want) {
							t.Fatalf("missing %q: %s", tc.want, out)
						}
					}
				}
			})
		}
	}
}
