package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type rejectingSessionAdmission struct{}

func (rejectingSessionAdmission) WithSessionTreeAdmission(context.Context, string, func() error) error {
	return lifecycle.ErrStopping
}

func TestStopSessionCancelsCatalogLineageAndRestoresAmbientRoot(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "start catalog workflow", err)

	testutil.FailErr(t, "stop session workflows", mgr.StopSession(ctx, "sess-1", "user stopped"))
	canceled, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "get canceled catalog run", err)
	if canceled.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("catalog status = %q, want canceled", canceled.Status)
	}
	active, err := mgr.GetActive(ctx, "sess-1")
	testutil.FailErr(t, "get replacement ambient", err)
	if active == nil || !mgr.IsAmbientRun(active) {
		t.Fatalf("active run = %+v, want fresh ambient root", active)
	}
}

func TestStopSessionLeavesAmbientRootRunning(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	ambient, err := mgr.StartAmbient(ctx, "sess-1", "implement", "1.0.0")
	testutil.FailErr(t, "start ambient", err)

	testutil.FailErr(t, "stop ambient-only session", mgr.StopSession(ctx, "sess-1", "user stopped"))
	active, err := mgr.GetActive(ctx, "sess-1")
	testutil.FailErr(t, "get ambient", err)
	if active == nil || active.ID != ambient.ID || !mgr.IsAmbientRun(active) {
		t.Fatalf("active run = %+v, want unchanged ambient %q", active, ambient.ID)
	}
}

func TestWorkflowStartRejectsDuringSessionStopButStopRepairRestoresAmbient(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "start catalog workflow", err)
	mgr.SessionAdmission = rejectingSessionAdmission{}

	if _, err := mgr.StartAmbient(ctx, "sess-1", "implement", "1.0.0"); !errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("start during stop admission = %v, want session stopping", err)
	}
	testutil.FailErr(t, "stop session workflows", mgr.StopSession(ctx, "sess-1", "user stopped"))
	canceled, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "get canceled catalog run", err)
	if canceled.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("catalog status = %q, want canceled", canceled.Status)
	}
	active, err := mgr.GetActive(ctx, "sess-1")
	testutil.FailErr(t, "get stop-triggered ambient repair", err)
	if active == nil || !mgr.IsAmbientRun(active) {
		t.Fatalf("active run = %+v, want stop-triggered ambient repair", active)
	}
}
