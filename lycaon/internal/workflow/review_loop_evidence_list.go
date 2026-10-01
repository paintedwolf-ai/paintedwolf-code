package workflow

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
)

// ListReviewLoopEvidence returns review-family gate records for a workflow run's
// current (or given) phase slot. Resolves the project host-data dir via EvidenceProjectDir.
func (m *RunManager) ListReviewLoopEvidence(ctx context.Context, sessionID, workflowRunID, phaseSlot string) ([]evidence.Record, error) {
	if m == nil || m.EvidenceStore == nil {
		return nil, nil
	}
	workflowRunID = strings.TrimSpace(workflowRunID)
	if workflowRunID == "" {
		return nil, nil
	}
	if phaseSlot == "" {
		run, err := m.Store.Get(ctx, workflowRunID)
		if err != nil {
			return nil, err
		}
		if run != nil {
			phaseSlot = run.CurrentPhase
		}
	}
	projectDir := ""
	if m.EvidenceProjectDir != nil {
		dir, err := m.EvidenceProjectDir(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		projectDir = dir
	}
	return inspector.ListReviewFamilyRecords(ctx, m.EvidenceStore, projectDir, workflowRunID, phaseSlot)
}

// ListReviewLoopEvidenceForType returns records of one review evidence type for a run/slot.
func (m *RunManager) ListReviewLoopEvidenceForType(
	ctx context.Context,
	sessionID, workflowRunID, phaseSlot string,
	gateType evidence.GateType,
) ([]evidence.Record, error) {
	if m == nil || m.EvidenceStore == nil {
		return nil, nil
	}
	projectDir := ""
	if m.EvidenceProjectDir != nil {
		dir, err := m.EvidenceProjectDir(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		projectDir = dir
	}
	return inspector.ListGateRecords(ctx, m.EvidenceStore, projectDir, workflowRunID, phaseSlot, []evidence.GateType{gateType})
}
