package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

type unavailableWorkerEvidence struct{ inspector.EvidenceStore }

func (unavailableWorkerEvidence) ReadAll(context.Context, string, string, string, evidence.GateType) ([]evidence.Record, error) {
	return nil, errors.New("fixture evidence store unavailable")
}

func TestWorkerValidationEvidenceUnavailablePreservesDelivery(t *testing.T) {
	mem := store.NewMemory()
	mgr := NewManager(mem, nil, nil, settings.DefaultSessionLimits())
	mgr.Verification.SetEvidenceStore(unavailableWorkerEvidence{})
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create session", err)
	task := &api.WorkerTask{ChildSessionID: sess.ID}
	proof := mgr.Workers.Summaries.Proof(t.Context(), task, nil, "")
	if !proof.Verification.Valid() || proof.Verification.Method != verification.Blocked {
		t.Fatalf("unavailable validation not recorded: %+v", proof)
	}
	proof.ChangedPaths = []string{"material"}
	report := workercompletion.EnrichWorkerCompletionReport(workercompletion.WorkerCompletionReport{LegStatus: "complete"}, proof, "complete")
	if report.LegStatus != "complete" || len(report.EvidenceObligations) != 1 ||
		report.EvidenceObligations[0].Status != workercompletion.EvidenceStatusUnmet ||
		report.EvidenceObligations[0].Reason != proof.Verification.Reason {
		t.Fatalf("delivery or limitation lost: %+v", report)
	}
}
