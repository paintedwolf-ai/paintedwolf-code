package workerharness

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type preparationFixture struct {
	t        *testing.T
	call     tools.ToolContext
	messages []api.Message
	receipts []workercompletion.WorkerInvocationReceipt
	run      tools.SourceRunCapture
	retain   bool
}

func (f *preparationFixture) PromptToolProfile(context.Context, *api.Session) (string, error) {
	return "implementer", nil
}
func (f *preparationFixture) Build(_ context.Context, s *api.Session, p string, _ inject.Machine) (tools.ToolContext, error) {
	if p != "implementer" {
		f.t.Fatalf("profile=%q", p)
	}
	return tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: s.ID}}, nil
}
func (f *preparationFixture) Enrich(_ context.Context, _ *api.Session, c tools.ToolContext) (tools.ToolContext, error) {
	c.Source.Roots = []projectroot.RootRef{{ID: "worker-root", Path: "/isolated/worker", IsPrimary: true}}
	return c, nil
}
func (f *preparationFixture) ActivePath(context.Context, *api.Session) (string, error) {
	return "/isolated/worker", nil
}
func (f *preparationFixture) Run(_ context.Context, name string, args map[string]any, c tools.ToolContext) (string, error) {
	f.call = c
	if c.Identity.WorkerJobID != "job" || c.Identity.ToolCallID == "" || c.Identity.MessageID == "" || c.ActiveRootPath() != "/isolated/worker" || c.Effects.Out == nil {
		f.t.Fatalf("unbound invocation=%+v", c)
	}
	if name == "verify" {
		if args["command"] != "check" {
			f.t.Fatalf("command=%v", args)
		}
		c.Effects.Out.SourceRun = &f.run
	} else if name != "read" || args["path"] != "material.txt" {
		f.t.Fatalf("unexpected invocation %s %v", name, args)
	}
	return "observed material", nil
}
func (f *preparationFixture) CommitEvidenceToolResult(_ context.Context, id, root, name string, args map[string]any, content string) (string, string, error) {
	if id != "child" || root != "/isolated/worker" || name != "read" || args["path"] != "material.txt" || content != "observed material" {
		f.t.Fatalf("unbound evidence: %s %s %s %v %s", id, root, name, args, content)
	}
	return "evidence:retained", "retained material", nil
}
func (f *preparationFixture) AppendMessages(_ context.Context, id string, msgs ...api.Message) error {
	if id != "child" {
		f.t.Fatalf("session=%s", id)
	}
	f.messages = append(f.messages, msgs...)
	return nil
}
func (f *preparationFixture) RecordSourceRunEvidence(_ context.Context, id string, s *api.Session, name string, r tools.SourceRunCapture) {
	if id != s.ID || name != "verify" {
		f.t.Fatalf("verification association %s %+v %s", id, s, name)
	}
	if f.retain {
		f.receipts = []workercompletion.WorkerInvocationReceipt{{ID: "receipt", CheckID: r.CheckID, IsCheck: r.IsCheck, Tool: "verify", Status: api.InvocationStatusCompleted, Verdict: string(r.Verdict), SourceRevision: "revision", SourceRootDigest: r.SourceRootDigest}}
	}
}
func (f *preparationFixture) WorkerSourceRuns(context.Context, string) ([]workercompletion.WorkerInvocationReceipt, error) {
	return f.receipts, nil
}
func (f *preparationFixture) SourceVerifyCommand(context.Context, string) string { return "" }
func (f *preparationFixture) WorkerRevision(context.Context, *api.WorkerTask) workercompletion.SourceRevision {
	return workercompletion.SourceRevision{Revision: "revision", RootDigest: "root"}
}
func (f *preparationFixture) service() *Service {
	return New(f, f, f, f, f, f, f, f, func(_ context.Context, _ *api.WorkerTask, _ []api.Message, _ string, r workercompletion.SourceRevision) workercompletion.WorkerCompletionProof {
		return workercompletion.WorkerCompletionProof{ChangedPaths: []string{"material.txt"}, SourceRevision: r.Revision, SourceRootDigest: r.RootDigest}
	})
}

func TestReadRetainsLinkedWorkerEvidence(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	f := &preparationFixture{t: t}
	handle, content, err := f.service().Read(t.Context(), &api.Session{ID: "child"}, &api.WorkerTask{ID: "job"}, "material.txt")
	if err != nil {
		t.Fatalf("read worker material: %v", err)
	}
	if handle != "evidence:retained" || content != "retained material" || len(f.messages) != 2 {
		t.Fatalf("read evidence=%q %q messages=%+v", handle, content, f.messages)
	}
	call, result := f.messages[0], f.messages[1]
	if call.Role != api.MessageRoleAssistant || len(call.ToolCalls) != 1 || call.ToolCalls[0].ID != f.call.Identity.ToolCallID || result.Role != api.MessageRoleTool || result.ToolResult.AssistantMessageID != call.ID || result.ToolResult.ToolCallID != call.ToolCalls[0].ID || result.Content != content || len(result.EvidenceHandles) != 1 || result.EvidenceHandles[0] != handle {
		t.Fatalf("unlinked transcript: %+v", f.messages)
	}
}

func TestVerifyRequiresRetainedCurrentTerminalReceipt(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	for _, tc := range []struct {
		name    string
		verdict string
		retain  bool
		digest  string
		want    string
	}{{"passed", api.SourceVerdictPassed, true, "root", ""}, {"failed", api.SourceVerdictFailed, true, "root", ""}, {"not retained", api.SourceVerdictPassed, false, "root", "not retained"}, {"stale source", api.SourceVerdictPassed, true, "stale", "prepared overlay validation"}, {"not source bound", api.SourceVerdictPassed, true, "", "source-bound terminal receipt"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &preparationFixture{t: t, retain: tc.retain, run: tools.SourceRunCapture{CheckID: "check", IsCheck: true, SourceRootDigest: tc.digest, Verdict: tc.verdict}}
			run, err := f.service().Verify(t.Context(), &api.Session{ID: "child"}, &api.WorkerTask{ID: "job", WorkspaceRoot: "/isolated/worker"}, "check")
			if tc.want == "" {
				if err != nil || run != &f.run {
					t.Fatalf("verify terminal receipt: %v %+v", err, run)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verify refusal=%v, want %q", err, tc.want)
			}
		})
	}
}

type fakeInvocationRecorder struct {
	invocation.Recorder
	started []invocation.Start
	settled map[string]invocation.Settlement
}

func (r *fakeInvocationRecorder) Begin(_ context.Context, start invocation.Start) (*api.InvocationReceipt, error) {
	r.started = append(r.started, start)
	return &api.InvocationReceipt{ID: "child-inv-1", ToolCallID: start.ToolCallID, Tool: start.ToolName, Status: api.InvocationStatusRunning}, nil
}

func (r *fakeInvocationRecorder) Settle(_ context.Context, id string, s invocation.Settlement) (*api.InvocationReceipt, error) {
	if r.settled == nil {
		r.settled = map[string]invocation.Settlement{}
	}
	r.settled[id] = s
	return &api.InvocationReceipt{ID: id, Status: s.Status}, nil
}

func TestVerifyOpensAndSettlesChildInvocationReceipt(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	f := &preparationFixture{t: t, retain: true, run: tools.SourceRunCapture{CheckID: "check", IsCheck: true, SourceRootDigest: "root", Verdict: api.SourceVerdictPassed}}
	rec := &fakeInvocationRecorder{}
	svc := f.service()
	svc.SetInvocations(rec)
	_, err := svc.Verify(t.Context(), &api.Session{ID: "child", ProjectID: "proj-1"}, &api.WorkerTask{ID: "job", WorkspaceRoot: "/isolated/worker"}, "check")
	if err != nil {
		t.Fatalf("Verify with invocation recorder failed: %v", err)
	}
	if len(rec.started) != 1 || rec.started[0].SessionID != "child" || rec.started[0].ToolName != "verify" {
		t.Fatalf("unexpected Begin calls: %+v", rec.started)
	}
	if f.call.Invocation.ID != "child-inv-1" || f.call.Invocation.SessionID != "child" {
		t.Fatalf("tool context did not receive child invocation receipt: %+v", f.call.Invocation)
	}
	st, ok := rec.settled["child-inv-1"]
	if !ok || st.Status != api.InvocationStatusCompleted || !st.Invoked || st.SourceVerdict != api.SourceVerdictPassed {
		t.Fatalf("unexpected settlement: ok=%v %+v", ok, st)
	}
}
