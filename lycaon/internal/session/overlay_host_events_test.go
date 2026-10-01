package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubOverlayPromoter struct {
	promoteOut api.WorkerMergeResult
	rejectOut  api.OverlayRejectOutcome
	promoteErr error
	rejectErr  error
}

func (s *stubOverlayPromoter) PromoteOverlay(_ context.Context, _, _ string, _ api.PromoteOverlayInput) (api.WorkerMergeResult, error) {
	return s.promoteOut, s.promoteErr
}
func (s *stubOverlayPromoter) RejectOverlay(_ context.Context, _, _, _ string) (api.OverlayRejectOutcome, error) {
	return s.rejectOut, s.rejectErr
}
func (s *stubOverlayPromoter) RebaseChildren(_ context.Context, _, _ string) ([]api.OverlayRebaseOutcome, error) {
	return nil, nil
}

func setupHostEventManager(t *testing.T, promoter OverlayPromoter) (*Manager, string) {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	store := store.NewMemory()
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{}, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	mgr := NewManager(store, nil, nil, settings.SessionLimits{})
	mgr.SetOverlayPromoter(promoter)
	return mgr, sess.ID
}

func transcriptContent(t *testing.T, mgr *Manager, sessionID string) []string {
	t.Helper()
	msgs, err := mgr.store.GetMessages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func TestManagerPromoteOverlayAppendsHostEvent(t *testing.T) {
	stub := &stubOverlayPromoter{
		promoteOut: api.WorkerMergeResult{
			OverlayPromotion: &api.OverlayPromotion{Files: []api.FileEditSnapshot{{Path: "shellsim/builtins.py", After: "merged"}}},
			Mode:             "merge",
			Status:           api.WorkerMergeStatusMerged,
			Applied:          []string{"shellsim/builtins.py"},
		},
	}
	mgr, sessionID := setupHostEventManager(t, stub)

	out, err := mgr.PromoteOverlay(context.Background(), sessionID, "ov-grep", api.PromoteOverlayInput{Detail: "hunks"})
	if err != nil {
		t.Fatalf("PromoteOverlay: %v", err)
	}
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
	messages, err := mgr.store.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "get promotion messages", err)
	if len(messages) != 1 || messages[0].ToolResult.OverlayPromotion == nil || messages[0].ToolResult.OverlayPromotion.Files[0].After != "merged" {
		t.Fatalf("promotion metadata missing from durable message: %#v", messages)
	}
	contents := transcriptContent(t, mgr, sessionID)
	if len(contents) != 1 {
		t.Fatalf("transcript len=%d want 1, got=%v", len(contents), contents)
	}
	if !strings.HasPrefix(contents[0], hostmarker.OverlayPromoteEventPrefix) {
		t.Fatalf("transcript[0]=%q want prefix %q", contents[0], hostmarker.OverlayPromoteEventPrefix)
	}
	if !strings.Contains(contents[0], `"merge_status":"merged"`) {
		t.Fatalf("transcript missing merged status payload: %q", contents[0])
	}
}

func TestManagerPromoteOverlayAppendsRebaseConflictBanner(t *testing.T) {
	stub := &stubOverlayPromoter{
		promoteOut: api.WorkerMergeResult{
			Mode:   "merge",
			Status: api.WorkerMergeStatusMerged,
			Rebased: []api.OverlayRebaseOutcome{
				{
					OverlayID: "ov-grep-followup",
					Status:    api.WorkerMergeStatusRebasing,
					Conflicts: []string{"shellsim/builtins.py"},
				},
			},
		},
	}
	mgr, sessionID := setupHostEventManager(t, stub)

	_, err := mgr.PromoteOverlay(context.Background(), sessionID, "ov-grep", api.PromoteOverlayInput{Detail: "hunks"})
	if err != nil {
		t.Fatalf("PromoteOverlay: %v", err)
	}
	contents := transcriptContent(t, mgr, sessionID)
	if len(contents) != 1 {
		t.Fatalf("transcript len=%d want 1", len(contents))
	}
	if !strings.HasPrefix(contents[0], hostmarker.OverlayPromoteEventPrefix) {
		t.Fatalf("transcript prefix missing: %q", contents[0])
	}
	if !strings.Contains(contents[0], "Code: OVERLAY_REBASE_CONFLICT") {
		t.Fatalf("transcript missing OVERLAY_REBASE_CONFLICT banner; got=%q", contents[0])
	}
	if !strings.Contains(contents[0], "ov-grep-followup") {
		t.Fatalf("transcript missing child overlay id in banner; got=%q", contents[0])
	}
}

func TestManagerRejectOverlayAppendsHostEventAndParentRejectedBanner(t *testing.T) {
	stub := &stubOverlayPromoter{
		rejectOut: api.OverlayRejectOutcome{
			OverlayID: "ov-grep",
			Reason:    "superseded",
			Orphaned:  []string{"ov-grep-followup", "ov-grep-tests"},
		},
	}
	mgr, sessionID := setupHostEventManager(t, stub)

	out, err := mgr.RejectOverlay(context.Background(), sessionID, "ov-grep", "superseded")
	if err != nil {
		t.Fatalf("RejectOverlay: %v", err)
	}
	if len(out.Orphaned) != 2 {
		t.Fatalf("orphaned=%v want 2", out.Orphaned)
	}
	contents := transcriptContent(t, mgr, sessionID)
	if len(contents) != 1 {
		t.Fatalf("transcript len=%d want 1", len(contents))
	}
	if !strings.HasPrefix(contents[0], hostmarker.OverlayRejectEventPrefix) {
		t.Fatalf("transcript prefix missing: %q", contents[0])
	}
	if !strings.Contains(contents[0], `"reason":"superseded"`) {
		t.Fatalf("transcript missing reason payload: %q", contents[0])
	}
	if got := strings.Count(contents[0], "Code: OVERLAY_PARENT_REJECTED"); got != 2 {
		t.Fatalf("OVERLAY_PARENT_REJECTED banner count=%d want 2; got=%q", got, contents[0])
	}
	for _, child := range []string{"ov-grep-followup", "ov-grep-tests"} {
		if !strings.Contains(contents[0], child) {
			t.Fatalf("transcript missing child %q in banners; got=%q", child, contents[0])
		}
	}
}

func TestManagerOverlayResolutionRetriesProjectPromotion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve func(*Manager, string) error
	}{
		{
			name: "promote",
			resolve: func(mgr *Manager, sessionID string) error {
				_, err := mgr.PromoteOverlay(context.Background(), sessionID, "overlay-1", api.PromoteOverlayInput{})
				return err
			},
		},
		{
			name: "reject",
			resolve: func(mgr *Manager, sessionID string) error {
				_, err := mgr.RejectOverlay(context.Background(), sessionID, "overlay-1", "not needed")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
			st := store.NewMemory()
			sess, err := st.Create(context.Background(), api.CreateSessionRequest{}, "project-1")
			testutil.FailErr(t, "create session", err)
			mgr := NewManager(st, nil, nil, settings.SessionLimits{})
			mgr.SetOverlayPromoter(&stubOverlayPromoter{
				promoteOut: api.WorkerMergeResult{JobID: "overlay-1", Status: api.WorkerMergeStatusMerged},
				rejectOut:  api.OverlayRejectOutcome{OverlayID: "overlay-1"},
			})
			var retriedProjectID string
			mgr.SetPromotionHook(func(_ context.Context, projectID string) {
				retriedProjectID = projectID
			})

			testutil.FailErr(t, "resolve overlay", tc.resolve(mgr, sess.ID))
			if retriedProjectID != "project-1" {
				t.Fatalf("retried project = %q want project-1", retriedProjectID)
			}
		})
	}
}

func TestManagerPromoteOverlayNoBannerWhenNoRebaseConflicts(t *testing.T) {
	stub := &stubOverlayPromoter{
		promoteOut: api.WorkerMergeResult{
			Mode:   "merge",
			Status: api.WorkerMergeStatusMerged,
			Rebased: []api.OverlayRebaseOutcome{
				{
					OverlayID: "ov-grep-followup",
					Status:    api.WorkerMergeStatusPending,
				},
			},
		},
	}
	mgr, sessionID := setupHostEventManager(t, stub)

	_, err := mgr.PromoteOverlay(context.Background(), sessionID, "ov-grep", api.PromoteOverlayInput{Detail: "hunks"})
	if err != nil {
		t.Fatalf("PromoteOverlay: %v", err)
	}
	contents := transcriptContent(t, mgr, sessionID)
	if len(contents) != 1 {
		t.Fatalf("transcript len=%d want 1", len(contents))
	}
	if strings.Contains(contents[0], "OVERLAY_REBASE_CONFLICT") {
		t.Fatalf("transcript should not include rebase conflict banner when no children conflicted: %q", contents[0])
	}
}

func TestFormatOverlayHostToolContent(t *testing.T) {
	content, err := FormatOverlayHostToolContent(hostmarker.OverlayPromoteEventPrefix, map[string]any{"job_id": "job-a"}, "")
	if err != nil {
		t.Fatalf("FormatOverlayHostToolContent: %v", err)
	}
	if !strings.HasPrefix(content, hostmarker.OverlayPromoteEventPrefix) {
		t.Fatalf("content = %q want promote prefix", content)
	}
	if !strings.Contains(content, "job-a") {
		t.Fatalf("content = %q want payload", content)
	}
}

func TestAppendHostEventPublishesOrd(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	store := store.NewMemory()
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)
	projectID := attachTestProject(t, mgr)

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: projectID}, projectID)
	testutil.FailErr(t, "create session", err)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	testutil.FailErr(t, "appendHostEvent", mgr.appendHostEvent(ctx, sess.ID, hostmarker.OverlayPromoteEventPrefix, map[string]any{"job_id": "j1"}, ""))

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 || msgs[0].Ord != 1 {
		t.Fatalf("store msgs = %+v want one row ord=1", msgs)
	}

	ev := waitParentMessageAppend(t, ch, sess.ID, msgs[0].ID)
	if ev.Message.Ord != 1 {
		t.Fatalf("SSE ord = %d want 1", ev.Message.Ord)
	}
}
