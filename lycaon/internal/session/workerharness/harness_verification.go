package workerharness

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type workspaceReceiptBinder interface {
	BindVerificationReceipt(jobID, childSessionID, workspacePath, branchRoot, checkID, checkpointID string, scopeRoots []string) (observation.VerificationBinding, bool)
}

// Verify uses the ordinary confined verifier and evidence recorder
// while the deterministic runner owns the worker's claim.
func (m *Service) Verify(ctx context.Context, child *api.Session, job *api.WorkerTask, command string) (*tools.SourceRunCapture, error) {
	if !configdir.IsHarnessChannel() {
		return nil, fmt.Errorf("worker preparation requires an isolated harness")
	}
	profile, err := m.Profiles.PromptToolProfile(ctx, child)
	if err != nil {
		return nil, err
	}
	tctx, err := m.Context.Build(ctx, child, profile, inject.Machine{})
	if err != nil {
		return nil, err
	}
	tctx.Identity.WorkerJobID = job.ID
	tctx, err = m.Workspaces.Enrich(ctx, child, tctx)
	if err != nil {
		return nil, err
	}
	tctx.Identity.ToolCallID = uuid.NewString()
	tctx.Identity.MessageID = uuid.NewString()
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	args := map[string]any{"command": command}
	if m.invocations != nil {
		contract, _ := toolcontract.Lookup("verify")
		argsDigest, digestErr := invocation.ArgsDigest(args)
		if digestErr != nil {
			return nil, digestErr
		}
		receipt, beginErr := m.invocations.Begin(ctx, invocation.Start{
			ProjectID:          child.ProjectID,
			SessionID:          child.ID,
			AssistantMessageID: tctx.Identity.MessageID,
			ToolCallID:         tctx.Identity.ToolCallID,
			ToolName:           "verify",
			Args:               args,
			Contract:           contract,
		})
		if beginErr != nil {
			return nil, beginErr
		}
		tctx.Invocation = tools.Invocation{
			ID:             receipt.ID,
			ProjectID:      child.ProjectID,
			SessionID:      child.ID,
			MessageID:      tctx.Identity.MessageID,
			ToolCallID:     tctx.Identity.ToolCallID,
			ToolName:       "verify",
			ArgsDigest:     argsDigest,
			ContractDigest: contract.Digest(),
			Contract:       contract,
		}
		defer func() {
			status := api.InvocationStatusCompleted
			if err != nil {
				status = api.InvocationStatusError
			}
			var rev, rootDigest, verdict string
			if tctx.Effects.Out != nil && tctx.Effects.Out.SourceRun != nil {
				rev = tctx.Effects.Out.SourceRun.SourceRevision
				rootDigest = tctx.Effects.Out.SourceRun.SourceRootDigest
				verdict = tctx.Effects.Out.SourceRun.Verdict
			}
			_, _ = m.invocations.Settle(context.WithoutCancel(ctx), receipt.ID, invocation.Settlement{
				Status:           status,
				Invoked:          true,
				SourceRevision:   rev,
				SourceRootDigest: rootDigest,
				SourceVerdict:    verdict,
			})
		}()
	}
	_, err = m.tools.Run(ctx, "verify", args, tctx)
	if err != nil {
		return nil, err
	}
	run := tctx.Effects.Out.SourceRun
	if run == nil || run.CheckID == "" || !run.IsCheck || run.SourceRootDigest == "" {
		return nil, fmt.Errorf("worker verification did not produce a source-bound terminal receipt")
	}
	m.Verification.RecordSourceRunEvidence(ctx, child.ID, child, "verify", *run)
	if run.Verdict == api.SourceVerdictPassed {
		if binder, ok := m.Workspaces.(workspaceReceiptBinder); ok {
			binder.BindVerificationReceipt(job.ID, child.ID, job.WorkspacePath, tctx.Source.WorkerBranchRoot, run.CheckID, run.SourceRevision, nil)
		}
	}
	if err := m.verifyPreparedWorkerEvidence(ctx, child, job, run); err != nil {
		return nil, err
	}
	return run, nil
}

func (m *Service) verifyPreparedWorkerEvidence(ctx context.Context, child *api.Session, job *api.WorkerTask, run *tools.SourceRunCapture) error {
	receipts, err := m.Verification.WorkerSourceRuns(ctx, child.ID)
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
	proof := m.proof(ctx, job, nil, m.Verification.SourceVerifyCommand(ctx, job.WorkspaceRoot), m.Verification.WorkerRevision(ctx, job)).WithSourceRuns(receipts)
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
