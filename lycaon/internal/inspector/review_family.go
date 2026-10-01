package inspector

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// ReviewFamilyEvidenceTypes are review_loop evidence keys that persist as gate records.
func ReviewFamilyEvidenceTypes() []evidence.GateType {
	return []evidence.GateType{
		evidence.GateTypeSurveyClaims,
		evidence.GateTypeSurveyChallenged,
		evidence.GateTypeOptionsJudge,
		evidence.GateTypePlanReview,
		evidence.GateTypePlanReviewAlt,
	}
}

// ListGateRecords reads all records for the given run/slot across gate types.
func ListGateRecords(
	ctx context.Context,
	store EvidenceStore,
	projectDir, runID, slot string,
	gateTypes []evidence.GateType,
) ([]evidence.Record, error) {
	if store == nil {
		return nil, nil
	}
	projectDir = strings.TrimSpace(projectDir)
	runID = strings.TrimSpace(runID)
	slot = strings.TrimSpace(slot)
	if projectDir == "" || runID == "" {
		return nil, nil
	}
	var out []evidence.Record
	for _, gt := range gateTypes {
		recs, err := store.ReadAll(ctx, projectDir, runID, slot, gt)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// ListReviewFamilyRecords lists review-family gate records for a workflow run + phase slot.
func ListReviewFamilyRecords(
	ctx context.Context,
	store EvidenceStore,
	projectDir, workflowRunID, phaseSlot string,
) ([]evidence.Record, error) {
	return ListGateRecords(ctx, store, projectDir, workflowRunID, phaseSlot, ReviewFamilyEvidenceTypes())
}
