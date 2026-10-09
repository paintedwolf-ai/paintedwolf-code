package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type queueRoundWorkerQueue struct {
	noopWorkerBranchClaim
	jobs []api.WorkerTask
}

type batchClaimFailStore struct {
	*store.Memory
	err error
}

type receiptTransitionStore struct {
	*store.Memory
	mu          sync.Mutex
	transitions []string
}

func (s *receiptTransitionStore) ClaimPromptSubmission(ctx context.Context, id string) (*store.PromptSubmission, bool, error) {
	row, claimed, err := s.Memory.ClaimPromptSubmission(ctx, id)
	if claimed {
		s.recordTransition("claim:" + id)
	}
	return row, claimed, err
}

func (s *receiptTransitionStore) ClaimPromptSubmissions(ctx context.Context, ids []string) ([]store.PromptSubmission, bool, error) {
	rows, claimed, err := s.Memory.ClaimPromptSubmissions(ctx, ids)
	if claimed {
		for _, id := range ids {
			s.recordTransition("claim:" + id)
		}
	}
	return rows, claimed, err
}

func (s *receiptTransitionStore) FinishPromptSubmission(ctx context.Context, id, token string, status store.PromptSubmissionStatus, result string, failure store.PromptSubmissionFailure) error {
	err := s.Memory.FinishPromptSubmission(ctx, id, token, status, result, failure)
	if err == nil {
		s.recordTransition("finish:" + id)
	}
	return err
}

func (s *receiptTransitionStore) recordTransition(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions = append(s.transitions, value)
}

func (s *batchClaimFailStore) ClaimPromptSubmissions(context.Context, []string) ([]store.PromptSubmission, bool, error) {
	return nil, false, s.err
}

func (q *queueRoundWorkerQueue) ListBySession(_ context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	var out []api.WorkerTask
	for _, job := range q.jobs {
		if job.ProjectID != projectID || job.ParentSessionID != sessionID {
			continue
		}
		for _, st := range status {
			if job.Status == st {
				out = append(out, job)
				break
			}
		}
	}
	return out, nil
}

func (q *queueRoundWorkerQueue) Get(jobID string) (*api.WorkerTask, bool) {
	for _, job := range q.jobs {
		if job.ID == jobID {
			cp := job
			return &cp, true
		}
	}
	return nil, false
}

func TestRunPromptSubmissionEnqueuesWhileBusy(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	operationID := uuid.NewString()
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, operationID, map[string]string{"text": "do this next"}, promptinput.Input{Text: "do this next"})
	testutil.FailErr(t, "admit prompt", err)

	dispatch := mgr.Submissions.DispatchLock(sess.ID)
	dispatch.Lock()
	defer dispatch.Unlock()

	resp, err := mgr.Submissions.RunPromptSubmission(ctx, row.ID)
	if err != nil {
		t.Fatalf("RunPromptSubmission while busy returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil response for an enqueued submission")
	}
	draft := mgr.queue.Snapshot(sess.ID)
	if len(draft.QueueItems) != 1 || draft.QueueItems[0].Text != "do this next" || draft.QueueItems[0].ID != row.ID {
		t.Fatalf("submission did not enqueue under its id: %+v", draft.QueueItems)
	}
	stored, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get submission", err)
	if stored.Status != store.PromptSubmissionQueued {
		t.Fatalf("receipt status = %s, want queued while waiting in the draft", stored.Status)
	}

	// Replay keeps one draft item.
	if _, err := mgr.Submissions.RunPromptSubmission(ctx, row.ID); err != nil {
		t.Fatalf("replayed RunPromptSubmission returned error: %v", err)
	}
	if got := mgr.queue.Snapshot(sess.ID); len(got.QueueItems) != 1 {
		t.Fatalf("replay duplicated the draft item: %+v", got.QueueItems)
	}
}

func TestRunPromptSubmissionRoutesConcurrentReceiptsInAdmissionOrder(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	inputs := []struct {
		id   string
		text string
		at   time.Time
	}{
		{id: uuid.NewString(), text: "accepted first", at: base},
		{id: uuid.NewString(), text: "accepted second", at: base.Add(time.Second)},
	}
	for _, item := range inputs {
		raw, marshalErr := json.Marshal(promptinput.Input{Text: item.text})
		testutil.FailErr(t, "encode input", marshalErr)
		_, _, putErr := st.PutPromptSubmission(ctx, store.PromptSubmission{
			ID: item.id, SessionID: sess.ID, ProjectID: sess.ProjectID,
			InputDigest: item.id, InputJSON: string(raw), Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
			CreatedAt: item.at,
		})
		testutil.FailErr(t, "put prompt submission", putErr)
	}

	lock := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
	lock.Lock()
	defer lock.Unlock()
	_, err = mgr.Submissions.RunPromptSubmission(ctx, inputs[1].id)
	testutil.FailErr(t, "run later receipt", err)

	draft := mgr.queue.Snapshot(sess.ID)
	if len(draft.QueueItems) != 2 || draft.QueueItems[0].ID != inputs[0].id || draft.QueueItems[1].ID != inputs[1].id {
		t.Fatalf("draft order = %+v, want durable admission order", draft.QueueItems)
	}
}

func TestNextReceiptClaimsOnlyAfterPriorReceiptIsTerminal(t *testing.T) {
	ctx := context.Background()
	st := &receiptTransitionStore{Memory: store.NewMemory()}
	registry := tools.NewStubRegistry()
	mgr := NewManager(st, llm.NewMockProvider(testMockConfig(t)), registry, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Guards.SetInvoker(testtool.RegistryInvoker{Registry: registry})
	mgr.SetDataDir(t.TempDir())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	firstInput := promptinput.Input{ContentParts: []api.MessageContentPart{{
		Content: "first", Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
	}}}
	first, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "first", firstInput)
	testutil.FailErr(t, "admit first", err)
	second, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "second", promptinput.Input{Text: "second"})
	testutil.FailErr(t, "admit second", err)

	_, err = mgr.Submissions.RunPromptSubmission(ctx, first.ID)
	testutil.FailErr(t, "run first", err)

	st.mu.Lock()
	got := append([]string(nil), st.transitions...)
	st.mu.Unlock()
	want := []string{"claim:" + first.ID, "finish:" + first.ID, "claim:" + second.ID, "finish:" + second.ID}
	if len(got) != len(want) {
		t.Fatalf("receipt transitions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("receipt transitions = %v, want %v", got, want)
		}
	}
}

func TestMalformedReceiptFailsThenDispatcherRunsNextReceipt(t *testing.T) {
	ctx := context.Background()
	st := &receiptTransitionStore{Memory: store.NewMemory()}
	registry := tools.NewStubRegistry()
	mgr := NewManager(st, llm.NewMockProvider(testMockConfig(t)), registry, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Guards.SetInvoker(testtool.RegistryInvoker{Registry: registry})
	mgr.SetDataDir(t.TempDir())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	firstID := uuid.NewString()
	first, _, err := st.PutPromptSubmission(ctx, store.PromptSubmission{
		ID: firstID, SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: firstID, InputJSON: "{", Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
	})
	testutil.FailErr(t, "put malformed receipt", err)
	second, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "second", promptinput.Input{Text: "second"})
	testutil.FailErr(t, "admit second", err)

	if _, runErr := mgr.Submissions.RunPromptSubmission(ctx, first.ID); runErr == nil {
		t.Fatal("malformed receipt unexpectedly succeeded")
	}

	first, err = st.GetPromptSubmission(ctx, first.ID)
	testutil.FailErr(t, "get malformed receipt", err)
	if first.Status != store.PromptSubmissionFailed {
		t.Fatalf("malformed receipt status = %s, want failed", first.Status)
	}
	second, err = st.GetPromptSubmission(ctx, second.ID)
	testutil.FailErr(t, "get next receipt", err)
	if second.Status != store.PromptSubmissionComplete {
		t.Fatalf("next receipt status = %s, want complete", second.Status)
	}
}

func TestQueueDrainRunsSubmissionReceiptLifecycle(t *testing.T) {
	mgr, st := newTestManager(t)
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	operationID := uuid.NewString()
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, operationID, map[string]string{"text": "queued work"}, promptinput.Input{Text: "queued work"})
	testutil.FailErr(t, "admit prompt", err)

	func() {
		lock := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
		lock.Lock()
		defer lock.Unlock()
		if _, err := mgr.Submissions.RunPromptSubmission(ctx, row.ID); err != nil {
			t.Fatalf("RunPromptSubmission while busy: %v", err)
		}
	}()

	mgr.Submissions.DrainQueue(ctx, sess.ID)

	if got := mgr.queue.Snapshot(sess.ID); len(got.QueueItems) != 0 {
		t.Fatalf("drain left items: %+v", got.QueueItems)
	}
	stored, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get submission", err)
	if stored.Status != store.PromptSubmissionComplete {
		t.Fatalf("receipt status = %s (error %q), want complete after drain", stored.Status, stored.Error)
	}
	msgs, err := mgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) == 0 || msgs[0].Role != api.MessageRoleUser || msgs[0].Content != "queued work" {
		t.Fatalf("queued turn did not run as the user prompt: %+v", msgs)
	}
}

func TestTakeQueuedSendPersistsContinuationWithoutOpeningNewTurn(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append opening user turn", st.AppendMessages(ctx, sess.ID, api.Message{
		ID: uuid.NewString(), Role: api.MessageRoleUser, Content: "Start with old.go",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted, Visibility: api.MessageVisibilityTranscript,
	}))
	testutil.FailErr(t, "mark session busy", st.UpdateSession(ctx, sess.ID, func(current *api.Session) {
		current.Status = api.SessionStatusBusy
	}))
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "send", promptinput.Input{Text: "Use new.go instead"})
	testutil.FailErr(t, "admit queued prompt", err)
	draft := mgr.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "Use new.go instead", row.AdmissionSeq, row.CreatedAt)
	draft, err = mgr.Drafts.Send(ctx, sess.ID, draft.Revision)
	testutil.FailErr(t, "request send", err)
	if !draft.Sending {
		t.Fatal("send reservation was not exposed in the draft")
	}

	messages, err := mgr.Submissions.TakeSend(ctx, sess.ID)
	testutil.FailErr(t, "take queued send", err)
	if len(messages) != 1 {
		t.Fatalf("sent messages = %d want 1", len(messages))
	}
	if messages[0].ID != row.ID || messages[0].Kind != api.MessageKindUserContinuation || messages[0].Content != "Use new.go instead" {
		t.Fatalf("sent continuation = %+v", messages[0])
	}
	if api.IsUserIntentMessage(messages[0]) || !api.IsUserInstructionMessage(messages[0]) {
		t.Fatalf("continuation boundary classification is wrong: %+v", messages[0])
	}
	turn, err := st.UserTurnOrdinal(ctx, sess.ID)
	testutil.FailErr(t, "read user turn ordinal", err)
	if turn != 1 {
		t.Fatalf("user turn ordinal = %d want 1", turn)
	}
	stored, err := st.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "read prompt receipt", err)
	if stored.Status != store.PromptSubmissionComplete {
		t.Fatalf("prompt receipt = %s want complete", stored.Status)
	}
	if after := mgr.Drafts.Snapshot(sess.ID); after.Sending || len(after.QueueItems) != 0 {
		t.Fatalf("queue after send = %+v", after)
	}
}

func TestQueueSendWithoutActivePromptLoopStartsContinuationCycle(t *testing.T) {
	ctx := t.Context()
	mgr, st := newTestManager(t)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append opening user turn", st.AppendMessages(ctx, sess.ID, api.Message{
		ID: uuid.NewString(), Role: api.MessageRoleUser, Content: "Begin the work",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted, Visibility: api.MessageVisibilityTranscript,
	}))
	testutil.FailErr(t, "mark session busy", st.UpdateSession(ctx, sess.ID, func(current *api.Session) {
		current.Status = api.SessionStatusBusy
	}))
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "send", promptinput.Input{Text: "Change the approach"})
	testutil.FailErr(t, "admit queued prompt", err)
	draft := mgr.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "Change the approach", row.AdmissionSeq, row.CreatedAt)
	_, err = mgr.Drafts.Send(ctx, sess.ID, draft.Revision)
	testutil.FailErr(t, "request send", err)
	persisted, err := st.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "read reserved receipt", err)
	var persistedInput promptinput.Input
	testutil.FailErr(t, "decode reserved receipt", json.Unmarshal([]byte(persisted.InputJSON), &persistedInput))
	if !persistedInput.Continuation {
		t.Fatal("send reservation did not persist continuation recovery intent")
	}

	drained, err := mgr.Submissions.DrainNext(ctx, sess.ID)
	testutil.FailErr(t, "drain continuation", err)
	if !drained {
		t.Fatal("reserved send was not drained")
	}
	messages, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	found := false
	for _, message := range messages {
		if message.ID == row.ID {
			found = true
			if message.Kind != api.MessageKindUserContinuation {
				t.Fatalf("queued message kind = %q want user_continuation", message.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("continuation row %s missing from transcript", row.ID)
	}
	turn, err := st.UserTurnOrdinal(ctx, sess.ID)
	testutil.FailErr(t, "read user turn ordinal", err)
	if turn != 1 {
		t.Fatalf("user turn ordinal = %d want 1", turn)
	}
}

func TestQueueRemoveCancelsSubmissionReceipt(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), map[string]string{"text": "drop me"}, promptinput.Input{Text: "drop me"})
	testutil.FailErr(t, "admit prompt", err)
	func() {
		lock := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
		lock.Lock()
		defer lock.Unlock()
		_, err := mgr.Submissions.RunPromptSubmission(ctx, row.ID)
		testutil.FailErr(t, "enqueue submission", err)
	}()

	draft := mgr.queue.Snapshot(sess.ID)
	if _, err := mgr.Drafts.Remove(ctx, sess.ID, draft.Revision, []string{row.ID}); err != nil {
		testutil.FailErr(t, "queue remove", err)
	}
	stored, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get submission", err)
	if stored.Status != store.PromptSubmissionCanceled {
		t.Fatalf("receipt status = %s, want canceled after removal", stored.Status)
	}
}

func TestMixedContentReceiptWaitsForPriorWorkerCycle(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	dir := t.TempDir()
	testdbseed.BindSessionWorkspace(t, st, sess.ID, dir)
	mgr.SetWorkerQueue(&queueRoundWorkerQueue{jobs: []api.WorkerTask{{
		ID: "job-1", ParentSessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: dir, Status: api.WorkerStatusRunning,
	}}})
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "mixed", promptinput.Input{
		Text: "inspect image", ArtifactIDs: []string{"artifact-1"},
	})
	testutil.FailErr(t, "admit mixed prompt", err)

	resp, err := mgr.Submissions.RunPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "route mixed prompt", err)
	if resp == nil || resp.MessageID != "" {
		t.Fatalf("response = %+v, want accepted queued response", resp)
	}
	stored, err := st.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get mixed receipt", err)
	if stored.Status != store.PromptSubmissionQueued {
		t.Fatalf("mixed receipt status = %s, want queued while prior worker cycle runs", stored.Status)
	}
}

func TestQueueableReceiptJoinsDraftWhilePriorWorkerCycleRuns(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	dir := t.TempDir()
	testdbseed.BindSessionWorkspace(t, st, sess.ID, dir)
	mgr.SetWorkerQueue(&queueRoundWorkerQueue{jobs: []api.WorkerTask{{
		ID: "job-1", ParentSessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: dir, Status: api.WorkerStatusRunning,
	}}})
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "next", promptinput.Input{Text: "run next"})
	testutil.FailErr(t, "admit prompt", err)

	resp, err := mgr.Submissions.RunPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "route prompt", err)
	if resp == nil || resp.MessageID != "" {
		t.Fatalf("response = %+v, want accepted queued response", resp)
	}
	draft := mgr.queue.Snapshot(sess.ID)
	if len(draft.QueueItems) != 1 || draft.QueueItems[0].ID != row.ID {
		t.Fatalf("draft = %+v, want waiting receipt", draft.QueueItems)
	}
}

func TestQueueRoundCompleteRequiresIdleWorkerCycle(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	dir := t.TempDir()
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)

	workers := &queueRoundWorkerQueue{}
	mgr.SetWorkerQueue(workers)

	if !mgr.Admission.RoundComplete(ctx, sess.ID) {
		t.Fatal("expected complete with no workers")
	}

	workers.jobs = append(workers.jobs, api.WorkerTask{
		ID:              "job-1",
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		Status: api.WorkerStatusPending,
	})

	if mgr.Admission.RoundComplete(ctx, sess.ID) {
		t.Fatal("expected incomplete while workers in flight")
	}
}

func TestDrainQueuedNextTurnRespectsHold(t *testing.T) {
	mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	const id = "sess-hold"

	draft := mgr.queue.AppendOrdered(id, "", testutil.HostOwner().ID, "queued", 0, time.Time{})
	if _, err := mgr.queue.SetHold(id, draft.Revision, true); err != nil {
		testutil.FailErr(t, "hold queue", err)
	}

	mgr.Submissions.DrainNext(context.Background(), id)

	if got := mgr.queue.Snapshot(id); len(got.QueueItems) != 1 {
		t.Fatalf("hold should have suppressed drain, draft = %+v", got.QueueItems)
	}
}

func TestDrainQueueRunsQueuedTurn(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := context.Background()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "queued work", promptinput.Input{Text: "queued work"})
	testutil.FailErr(t, "admit queued prompt", err)
	mgr.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "queued work", 0, time.Time{})
	mgr.Submissions.DrainQueue(ctx, sess.ID)

	if got := mgr.queue.Snapshot(sess.ID); len(got.QueueItems) != 0 {
		t.Fatalf("drain left items: %+v", got.QueueItems)
	}
	msgs, err := mgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) < 2 {
		t.Fatalf("queued turn did not run, messages = %d", len(msgs))
	}
	if msgs[0].Role != api.MessageRoleUser || msgs[0].Content != "queued work" {
		t.Fatalf("first message is not the queued user prompt: %+v", msgs[0])
	}
	closed, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get drained receipt", err)
	if closed.Status != store.PromptSubmissionComplete {
		t.Fatalf("drained receipt status = %s, want complete", closed.Status)
	}
}

func TestDrainQueueRetainsItemWithoutReceipt(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := context.Background()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	mgr.queue.AppendOrdered(sess.ID, "", testutil.HostOwner().ID, "orphan work", 0, time.Time{})
	mgr.Submissions.DrainQueue(ctx, sess.ID)
	if draft := mgr.queue.Snapshot(sess.ID); len(draft.QueueItems) != 1 || draft.QueueItems[0].Text != "orphan work" {
		t.Fatalf("receiptless item disappeared from draft: %+v", draft.QueueItems)
	}

	msgs, err := mgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) != 0 {
		t.Fatalf("receiptless item ran anyway, messages = %d", len(msgs))
	}
}

func TestDrainQueueRetainsDraftWhenReceiptClaimFails(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("receipt store unavailable")
	st := &batchClaimFailStore{Memory: store.NewMemory(), err: wantErr}
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "queued work", promptinput.Input{Text: "queued work"})
	testutil.FailErr(t, "admit prompt", err)
	draft := mgr.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "queued work", row.AdmissionSeq, row.CreatedAt)

	mgr.Submissions.DrainQueue(ctx, sess.ID)

	after := mgr.queue.Snapshot(sess.ID)
	if after.Revision != draft.Revision || len(after.QueueItems) != 1 || after.QueueItems[0].ID != row.ID {
		t.Fatalf("failed claim changed draft: before=%+v after=%+v", draft, after)
	}
	stored, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get prompt submission", err)
	if stored.Status != store.PromptSubmissionQueued {
		t.Fatalf("receipt status = %s, want queued after failed atomic claim", stored.Status)
	}
}

func TestQueueHeadWaitsForEarlierMixedContentReceipt(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	put := func(id string, in promptinput.Input, at time.Time) *store.PromptSubmission {
		raw, marshalErr := json.Marshal(in)
		testutil.FailErr(t, "encode prompt input", marshalErr)
		row, _, putErr := st.PutPromptSubmission(ctx, store.PromptSubmission{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: id,
			InputJSON: string(raw), Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID, CreatedAt: at,
		})
		testutil.FailErr(t, "put prompt submission", putErr)
		return row
	}
	mixed := put(uuid.NewString(), promptinput.Input{Text: "inspect image", ArtifactIDs: []string{"artifact-1"}}, base)
	text := put(uuid.NewString(), promptinput.Input{Text: "run second"}, base.Add(time.Second))
	mgr.queue.AppendOrdered(sess.ID, text.ID, text.SubmittedBy, "run second", text.AdmissionSeq, text.CreatedAt)

	dispatchable, err := mgr.Submissions.HeadDispatchable(ctx, sess.ID)
	testutil.FailErr(t, "check queue head", err)
	if dispatchable {
		t.Fatal("text draft became dispatchable before the mixed-content receipt")
	}

	draft := mgr.queue.Snapshot(sess.ID)
	if len(draft.QueueItems) != 1 || draft.QueueItems[0].ID != text.ID {
		t.Fatalf("later text draft changed while mixed receipt was first: %+v", draft.QueueItems)
	}
	for _, id := range []string{mixed.ID, text.ID} {
		row, getErr := st.GetPromptSubmission(ctx, id)
		testutil.FailErr(t, "get queued prompt submission", getErr)
		if row.Status != store.PromptSubmissionQueued {
			t.Fatalf("submission %s status = %s, want queued", id, row.Status)
		}
	}
}

func TestDrainQueueReplaysStoredInput(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := context.Background()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	stored := promptinput.Input{Text: "stored prose"}
	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), stored, stored)
	testutil.FailErr(t, "admit queued prompt", err)
	// The receipt records executable input.
	mgr.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "stale draft preview", 0, time.Time{})
	mgr.Submissions.DrainQueue(ctx, sess.ID)

	msgs, err := mgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) == 0 || msgs[0].Content != "stored prose" {
		t.Fatalf("turn did not run from the stored input, messages = %+v", msgs)
	}
}

func TestQueueUpdateTextRewritesStoredInput(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := context.Background()
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "first draft", promptinput.Input{Text: "first draft"})
	testutil.FailErr(t, "admit queued prompt", err)
	draft := mgr.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "first draft", 0, time.Time{})

	_, err = mgr.Drafts.UpdateText(ctx, sess.ID, draft.Revision, row.ID, "edited text")
	testutil.FailErr(t, "queue update text", err)

	stored, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get edited receipt", err)
	var in promptinput.Input
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(stored.InputJSON), &in))
	if in.Text != "edited text" {
		t.Fatalf("stored input text = %q, want the edited text", in.Text)
	}

	mgr.Submissions.DrainQueue(ctx, sess.ID)
	msgs, err := mgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) == 0 || msgs[0].Content != "edited text" {
		t.Fatalf("drain ran the pre-edit text: %+v", msgs)
	}
}

func TestCoalescePromptInputs(t *testing.T) {
	got := promptinput.Coalesce([]promptinput.Input{
		{Text: "a"}, {Text: "b"}, {Text: "  "}, {Text: "c"},
	})
	want := "a\n\nb\n\nc"
	if got.Text != want {
		t.Fatalf("coalesce = %q, want %q", got.Text, want)
	}
}

func TestPromptRidesQueueAcceptsOnlyEditableText(t *testing.T) {
	tests := []struct {
		name string
		in   promptinput.Input
		want bool
	}{
		{name: "text", in: promptinput.Input{Text: "next"}, want: true},
		{name: "blank", in: promptinput.Input{Text: "  "}},
		{name: "artifact", in: promptinput.Input{Text: "next", ArtifactIDs: []string{"artifact"}}},
		{name: "content part", in: promptinput.Input{Text: "next", ContentParts: []api.MessageContentPart{{Content: "part"}}}},
		{name: "tool profile", in: promptinput.Input{Text: "next", ToolProfile: "editor"}},
		{name: "write root", in: promptinput.Input{Text: "next", WritePinRootID: "root"}},
		{name: "write glob", in: promptinput.Input{Text: "next", WritePinGlobs: []string{"file.go"}}},
		{name: "host signal", in: promptinput.Input{Text: "next", HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostKick}}},
		{name: "closeout", in: promptinput.Input{Text: "next", ProseFinish: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.in.RidesQueue(); got != test.want {
				t.Fatalf("promptRidesQueue() = %v, want %v", got, test.want)
			}
		})
	}
}

func (*queueRoundWorkerQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
