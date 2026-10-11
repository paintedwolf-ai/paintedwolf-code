package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/pkg/api"
)

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
