package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// receiptRecorder records admitted receipt ids. Host turns mint their own operation id.
type receiptRecorder struct {
	*store.Memory
	mu  sync.Mutex
	ids []string
}

func (r *receiptRecorder) PutPromptSubmission(ctx context.Context, in store.PromptSubmission) (*store.PromptSubmission, bool, error) {
	row, created, err := r.Memory.PutPromptSubmission(ctx, in)
	if err == nil && created {
		r.mu.Lock()
		r.ids = append(r.ids, row.ID)
		r.mu.Unlock()
	}
	return row, created, err
}

func (r *receiptRecorder) admitted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func newReceiptTestManager(t *testing.T) (*Manager, *receiptRecorder) {
	t.Helper()
	recorder := &receiptRecorder{Memory: store.NewMemory()}
	registry := tools.NewStubRegistry()
	mgr := NewManager(recorder, llm.NewMockProvider(testMockConfig(t)), registry, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetToolInvoker(testtool.RegistryInvoker{Registry: registry})
	return mgr, recorder
}

func newReceiptTestSession(t *testing.T, mgr *Manager, sessions *receiptRecorder) *api.Session {
	t.Helper()
	sess, err := sessions.Create(context.Background(), api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_ = mgr
	return sess
}

func TestHostTurnWritesReceipt(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := workercontext.WithJob(context.Background(), "worker-job-1")
	sess := newReceiptTestSession(t, mgr, sessions)

	_, err := mgr.PromptHostTurn(ctx, sess.ID, store.PromptSubmissionOriginWorkerCloseout, "closeout kick")
	testutil.FailErr(t, "run host turn", err)

	ids := sessions.admitted()
	if len(ids) != 1 {
		t.Fatalf("host turn admitted %d receipts, want 1", len(ids))
	}
	row, err := mgr.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get host receipt", err)
	if row.Origin != store.PromptSubmissionOriginWorkerCloseout {
		t.Fatalf("receipt origin = %q, want worker_closeout", row.Origin)
	}
	if row.Status != store.PromptSubmissionComplete {
		t.Fatalf("receipt status = %q, want complete", row.Status)
	}
	var stored PromptInput
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(row.InputJSON), &stored))
	if stored.Text != "closeout kick" {
		t.Fatalf("stored input text = %q, want the kick text", stored.Text)
	}
	if stored.WorkerJobID != "worker-job-1" {
		t.Fatalf("stored worker_job_id = %q", stored.WorkerJobID)
	}
	if stored.HostSignal == nil || stored.HostSignal.Kind != api.MessageKindHostKick ||
		stored.HostSignal.ID != string(anchor.WorkerCloseout) {
		t.Fatalf("stored host signal = %+v, want worker.closeout kick", stored.HostSignal)
	}
	if !stored.ProseFinish {
		t.Fatal("worker closeout must persist ProseFinish so replay gates invocation")
	}

	msgs, err := sessions.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	var kick api.Message
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Origin == api.MessageOriginHost {
			kick = msg
			break
		}
	}
	if kick.ID == "" {
		t.Fatal("worker closeout must persist a host user row")
	}
	if kick.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("closeout visibility = %q, want internal", kick.Visibility)
	}
	if kick.Kind != api.MessageKindHostKick || kick.HostSignalID != string(anchor.WorkerCloseout) {
		t.Fatalf("closeout row kind/signal = %q/%q", kick.Kind, kick.HostSignalID)
	}
	if kick.WorkerID != "worker-job-1" {
		t.Fatalf("closeout worker_job_id = %q", kick.WorkerID)
	}
}

func TestHostTurnGroundingRetryKeepsTools(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := context.Background()
	sess := newReceiptTestSession(t, mgr, sessions)

	_, err := mgr.PromptHostTurn(ctx, sess.ID, store.PromptSubmissionOriginGroundingRetry, "retry kick")
	testutil.FailErr(t, "run grounding retry", err)

	ids := sessions.admitted()
	if len(ids) != 1 {
		t.Fatalf("grounding retry admitted %d receipts, want 1", len(ids))
	}
	row, err := mgr.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get grounding receipt", err)
	var stored PromptInput
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(row.InputJSON), &stored))
	if stored.ProseFinish {
		t.Fatal("grounding retry must keep tools")
	}
	if stored.HostSignal == nil || stored.HostSignal.Kind != api.MessageKindHostKick ||
		stored.HostSignal.ID != string(anchor.WorkerCitationGrounding) {
		t.Fatalf("stored host signal = %+v, want worker.citation.grounding kick", stored.HostSignal)
	}
}

func TestHostLoopWakeReceiptCarriesHostSignal(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := context.Background()
	sess := newReceiptTestSession(t, mgr, sessions)

	_, err := mgr.promptHostLoopWake(ctx, sess.ID)
	testutil.FailErr(t, "run loop wake", err)

	ids := sessions.admitted()
	if len(ids) != 1 {
		t.Fatalf("loop wake admitted %d receipts, want 1", len(ids))
	}
	row, err := mgr.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get loop wake receipt", err)
	if row.Origin != store.PromptSubmissionOriginLoopWake {
		t.Fatalf("receipt origin = %q, want loop_wake", row.Origin)
	}
	var stored PromptInput
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(row.InputJSON), &stored))
	if stored.HostSignal == nil || stored.HostSignal.Kind != api.MessageKindHostLoopWake {
		t.Fatalf("stored host signal = %+v, want a loop-wake signal", stored.HostSignal)
	}
}

func TestRecoveryResolvesHostReceiptsWithoutReplay(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := context.Background()
	sess := newReceiptTestSession(t, mgr, sessions)

	userRow, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "user work", PromptInput{Text: "user work"})
	testutil.FailErr(t, "admit user prompt", err)
	hostRow, _, err := mgr.admitPrompt(ctx, sess.ID, uuid.NewString(),
		store.PromptSubmissionOriginGroundingRetry, "retry kick", PromptInput{Text: "retry kick"})
	testutil.FailErr(t, "admit host prompt", err)

	recovered, err := mgr.RecoverPromptSubmissions(ctx)
	testutil.FailErr(t, "recover submissions", err)
	if len(recovered) != 1 || recovered[0] != userRow.ID {
		t.Fatalf("recovered = %v, want only the user receipt %s", recovered, userRow.ID)
	}

	resolved, err := mgr.GetPromptSubmission(ctx, hostRow.ID)
	testutil.FailErr(t, "get host receipt", err)
	if resolved.Status != store.PromptSubmissionInterrupted {
		t.Fatalf("host receipt status = %q, want interrupted", resolved.Status)
	}
	if resolved.Error == "" {
		t.Fatal("an interrupted host receipt must record why it was not replayed")
	}
}
