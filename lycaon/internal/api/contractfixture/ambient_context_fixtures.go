package contractfixture

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func MustOpenWorkflowTestDB(t *testing.T) db.ReadHandle {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ambient-wf.db")
	return sqlDB
}

func WaitAmbientActiveRun(t *testing.T, wfMgr *workflow.RunManager, sessionID string) *wire.WorkflowRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := wfMgr.Store.Runs.ActiveBySession(context.Background(), sessionID)
		testutil.FailErr(t, "GetActive", err)
		if run != nil {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected active implement workflow run after session create")
	return nil
}

type WarmTrackingRepo struct {
	Warmed     []string
	BriefCalls int
}

func (p *WarmTrackingRepo) Brief(context.Context, string) (*repoinfo.Brief, error) {
	p.BriefCalls++
	return &repoinfo.Brief{}, nil
}

func (p *WarmTrackingRepo) Warm(path string) { p.Warmed = append(p.Warmed, path) }

func (*WarmTrackingRepo) Close() error { return nil }

func (*WarmTrackingRepo) KnownEmpty(context.Context, string) (bool, error) { return false, nil }
func (*WarmTrackingRepo) Changed(context.Context, string)                  {}
func (*WarmTrackingRepo) SetOnSettled(func(string))                        {}
