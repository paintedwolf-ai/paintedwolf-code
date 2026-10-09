package session

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// VerifyHarnessWorker uses the ordinary confined verifier and evidence recorder
// while the deterministic runner owns the worker's claim.
func (m *Manager) VerifyHarnessWorker(ctx context.Context, child *api.Session, job *api.WorkerTask, command string) (*tools.SourceRunCapture, error) {
	if !configdir.IsHarnessChannel() {
		return nil, fmt.Errorf("worker preparation requires an isolated harness")
	}
	profile, err := m.promptToolProfile(ctx, child)
	if err != nil {
		return nil, err
	}
	tctx, err := m.buildToolContext(ctx, child, profile, inject.Machine{})
	if err != nil {
		return nil, err
	}
	tctx.Identity.WorkerJobID = job.ID
	tctx, err = m.EnrichWorkerToolContext(ctx, child, tctx)
	if err != nil {
		return nil, err
	}
	tctx.Identity.ToolCallID = uuid.NewString()
	tctx.Identity.MessageID = uuid.NewString()
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	_, err = m.tools.Run(ctx, "verify", map[string]any{"command": command}, tctx)
	if err != nil {
		return nil, err
	}
	run := tctx.Effects.Out.SourceRun
	if run == nil || run.CheckID == "" || !run.IsCheck || run.SourceRootDigest == "" {
		return nil, fmt.Errorf("worker verification did not produce a source-bound terminal receipt")
	}
	m.recordSourceRunEvidence(ctx, child.ID, child, "verify", *run)
	if err := m.verifyPreparedWorkerEvidence(ctx, child, job, run); err != nil {
		return nil, err
	}
	return run, nil
}

func (m *Manager) verifyPreparedWorkerEvidence(ctx context.Context, child *api.Session, job *api.WorkerTask, run *tools.SourceRunCapture) error {
	receipts, err := m.WorkerSourceRuns(ctx, child.ID)
	if err != nil {
		return err
	}
	found := false
	for _, receipt := range receipts {
		found = found || receipt.CheckID == run.CheckID
	}
	if !found {
		return fmt.Errorf("worker validation was not retained by the evidence store")
	}
	proof := OverlaySourceProof(ctx, job, nil, m.SourceVerifyCommand(ctx, job.WorkspaceRoot), m.WorkerVerificationRevision(ctx, job)).WithSourceRuns(receipts)
	status, reason, _ := workercompletion.SourceEvidenceView(proof)
	expected := workercompletion.EvidenceStatusUnmet
	if run.Verdict == api.SourceVerdictPassed {
		expected = workercompletion.EvidenceStatusSatisfied
	}
	if status != expected {
		return fmt.Errorf("prepared overlay validation is %s: %s", status, reason)
	}
	return nil
}
