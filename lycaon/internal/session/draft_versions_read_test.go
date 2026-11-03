package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMessageCarriesVersionCount(t *testing.T) {
	store := store.NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: testdbseed.DefaultProjectID}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	slotID := "slot-1"
	if _, err := store.AppendDraftVersion(ctx, sess.ID, slotID, "v0", "CODE_A"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	msg := api.Message{
		ID:                slotID,
		Role:              api.MessageRoleAssistant,
		Content:           "final",
		Kind:              api.MessageKindDraft,
		DraftStatus:       api.DraftStatusCommitted,
		DraftVersionCount: 2,
		CreatedAt:         time.Now().UTC(),
	}
	if err := store.AppendMessages(ctx, sess.ID, msg); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 || msgs[0].DraftVersionCount != 2 {
		t.Fatalf("messages = %+v want draft_version_count=2", msgs)
	}
}

func TestDraftVersionsEndpointOrder(t *testing.T) {
	store := store.NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: testdbseed.DefaultProjectID}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v0", "A"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion v0", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v1", "B"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion v1", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v2", "C"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion v2", err)
	}
	got, err := store.ListDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "ListDraftVersions", err)
	if len(got) != 3 || got[0].Body != "v0" || got[2].OutcomeCode != "C" {
		t.Fatalf("versions = %+v", got)
	}
}

func TestDraftNeverEmitsDeleteOpForCoordinatorDraftSlot(t *testing.T) {
	store := store.NewMemory()
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetEventPublisher(pub)
	projectID := attachTestProject(t, mgr)

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: projectID}, projectID)
	testutil.FailErr(t, "Create", err)

	slotID := "draft-slot-1"
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	defer unsub()

	placeholder := api.Message{
		ID:          slotID,
		Role:        api.MessageRoleAssistant,
		Content:     "attempt one",
		Kind:        api.MessageKindDraft,
		DraftStatus: api.DraftStatusLive,
		Visibility:  api.MessageVisibilityInternal,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.AppendMessages(ctx, sess.ID, placeholder); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, slotID, "attempt one", "SYNTH_HANDLE_NOT_IN_LEGS"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	reset := placeholder
	reset.Content = ""
	if err := mgr.Runner.Transcript.Update(ctx, sess.ID, slotID, reset); err != nil {
		testutil.FailErr(t, "updateMessage", err)
	}

	pub.FlushMessagePatches(ctx, sess.ID)
	testutil.FailErr(t, "drain publisher", pub.Close(t.Context()))
	hub.FlushDebounced()
	patches := 0
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("hub closed before draft events were inspected")
			}
			if env.Topic != api.EventTopicMessage {
				continue
			}
			var ev api.MessageEvent
			testutil.FailErr(t, "decode draft event", json.Unmarshal(env.Data, &ev))
			if ev.Message.ID != slotID {
				continue
			}
			if ev.Op != api.MessageChangePatch {
				t.Fatalf("draft slot emitted unexpected op %q: %+v", ev.Op, ev)
			}
			patches++
		default:
			if patches != 1 {
				t.Fatalf("draft slot emitted %d patches, want 1", patches)
			}
			return
		}
	}
}

func TestSQLStoreDraftVersionsUnknownSlotEmpty(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "draft-versions-read.db")

	store := store.NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "body", "CODE"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	got, err := store.ListDraftVersions(ctx, sess.ID, "unknown-slot")
	testutil.FailErr(t, "ListDraftVersions unknown slot", err)
	if len(got) != 0 {
		t.Fatalf("unknown slot versions = %+v want empty", got)
	}
}

func TestEditorConfigMismatchCardAnnotatesTheLandedWrite(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-editorconfig"}
	raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.EditorConfigMismatchCode, map[string]any{
		"path":               "tests/test_image.py",
		"editorconfig_rules": []string{"trim_trailing_whitespace", "indent_style"},
		"editorconfig_lines": "tests/test_image.py:16,28 trim_trailing_whitespace; tests/test_image.py:16,28 indent_style",
	}, &api.FeedbackSubject{Kind: "file", ID: "tests/test_image.py"})

	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "write",
		map[string]any{"path": "tests/test_image.py"}, "Wrote 1200 bytes to tests/test_image.py", 1, raised)
	for _, want := range []string{
		"Wrote 1200 bytes to tests/test_image.py",
		"Code: EDITORCONFIG_MISMATCH",
		"(trim_trailing_whitespace, indent_style)",
		"tests/test_image.py:16,28 indent_style",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(toolrejection.EditorConfigMismatchCode) {
		t.Fatalf("facts = %#v, want the mismatch code", facts)
	}

	plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "write",
		map[string]any{"path": "tests/test_image.py"}, "Wrote 1200 bytes", 1, guidance.ToolResultFacts{})
	if strings.Contains(plain, "EDITORCONFIG_MISMATCH") {
		t.Fatalf("a write with no stated mismatch carried the card:\n%s", plain)
	}
}

// stubWorkerCycleLister implements WorkerCycleLister for the stacked-base tests.
// Only Get is exercised — workeradmission.ValidateStackedBase does not enumerate the queue.
type stubWorkerCycleLister struct {
	noopWorkerBranchClaim
	tasks map[string]*api.WorkerTask
}

func (s *stubWorkerCycleLister) Get(jobID string) (*api.WorkerTask, bool) {
	t, ok := s.tasks[jobID]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

func (s *stubWorkerCycleLister) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func writeTask(id, sessionID string, status api.WorkerMergeStatus) *api.WorkerTask {
	return &api.WorkerTask{
		ID:              id,
		ParentSessionID: sessionID,
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x.go"}},
		MergeStatus:     status,
	}
}

func TestValidateStackedBaseEmptyBaseAccepts(t *testing.T) {
	r, err := workeradmission.ValidateStackedBase(context.Background(), nil, "sess-1", "/p", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}})
	if err != nil || r.Code != "" {
		t.Fatalf("empty base must accept; got code=%q err=%v", r.Code, err)
	}
}

func TestValidateStackedBaseMissingOverlayRejects(t *testing.T) {
	lister := &stubWorkerCycleLister{tasks: map[string]*api.WorkerTask{}}
	r, err := workeradmission.ValidateStackedBase(
		context.Background(), lister, "sess-1", "/p",
		api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}, BaseOverlayID: "ov-ghost"},
	)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Code != workeradmission.OverlayBaseMissingCode {
		t.Fatalf("code=%q want %q", r.Code, workeradmission.OverlayBaseMissingCode)
	}
}

func TestValidateStackedBaseWrongSessionRejects(t *testing.T) {
	lister := &stubWorkerCycleLister{tasks: map[string]*api.WorkerTask{
		"ov-foreign": writeTask("ov-foreign", "other-sess", api.WorkerMergeStatusPending),
	}}
	r, err := workeradmission.ValidateStackedBase(
		context.Background(), lister, "sess-1", "/p",
		api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}, BaseOverlayID: "ov-foreign"},
	)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Code != workeradmission.OverlayBaseMissingCode {
		t.Fatalf("code=%q want %q (wrong-session must reject as missing)", r.Code, workeradmission.OverlayBaseMissingCode)
	}
	if r.Data["reason"] != "wrong_parent_session" {
		t.Fatalf("reason=%v want wrong_parent_session", r.Data["reason"])
	}
}

func TestValidateStackedBaseReadScopeRejects(t *testing.T) {
	readScopeTask := &api.WorkerTask{
		ID:              "ov-read",
		ParentSessionID: "sess-1",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeRead},
		MergeStatus:     api.WorkerMergeStatusPending,
	}
	lister := &stubWorkerCycleLister{tasks: map[string]*api.WorkerTask{"ov-read": readScopeTask}}
	r, err := workeradmission.ValidateStackedBase(
		context.Background(), lister, "sess-1", "/p",
		api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}, BaseOverlayID: "ov-read"},
	)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Code != workeradmission.OverlayBaseMissingCode || r.Data["reason"] != "base_is_read_scope" {
		t.Fatalf("got code=%q reason=%v; want %s base_is_read_scope", r.Code, r.Data["reason"], workeradmission.OverlayBaseMissingCode)
	}
}

func TestValidateStackedBasePendingAccepts(t *testing.T) {
	lister := &stubWorkerCycleLister{tasks: map[string]*api.WorkerTask{
		"ov-parent": writeTask("ov-parent", "sess-1", api.WorkerMergeStatusPending),
	}}
	r, err := workeradmission.ValidateStackedBase(
		context.Background(), lister, "sess-1", "/p",
		api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}, BaseOverlayID: "ov-parent"},
	)
	if err != nil || r.Code != "" {
		t.Fatalf("pending base must accept; got code=%q err=%v", r.Code, err)
	}
}

func TestValidateStackedBaseTerminalRejects(t *testing.T) {
	terminal := []api.WorkerMergeStatus{
		api.WorkerMergeStatusMerged,
		api.WorkerMergeStatusRejected,
		api.WorkerMergeStatusOrphaned,
		api.WorkerMergeStatusAborted,
	}
	for _, status := range terminal {
		t.Run(string(status), func(t *testing.T) {
			lister := &stubWorkerCycleLister{tasks: map[string]*api.WorkerTask{
				"ov-closed": writeTask("ov-closed", "sess-1", status),
			}}
			r, err := workeradmission.ValidateStackedBase(
				context.Background(), lister, "sess-1", "/p",
				api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}, BaseOverlayID: "ov-closed"},
			)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if r.Code != workeradmission.OverlayBaseNotPendingCode {
				t.Fatalf("status=%s got code=%q want %q", status, r.Code, workeradmission.OverlayBaseNotPendingCode)
			}
		})
	}
}

func TestValidateStackedBaseRebasingAccepts(t *testing.T) {
	// A child may dispatch onto a base that is currently rebasing (parent of its
	// parent just promoted) — rebasing is a non-terminal state.
	lister := &stubWorkerCycleLister{tasks: map[string]*api.WorkerTask{
		"ov-rebasing": writeTask("ov-rebasing", "sess-1", api.WorkerMergeStatusRebasing),
	}}
	r, err := workeradmission.ValidateStackedBase(
		context.Background(), lister, "sess-1", "/p",
		api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x"}, BaseOverlayID: "ov-rebasing"},
	)
	if err != nil || r.Code != "" {
		t.Fatalf("rebasing base must accept; got code=%q err=%v", r.Code, err)
	}
}

func (*stubWorkerCycleLister) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
