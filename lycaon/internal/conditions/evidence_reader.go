package conditions

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
)

// StoreEvidenceReader adapts inspector.EvidenceStore for core evidence predicates.
type StoreEvidenceReader struct {
	Store inspector.EvidenceStore
}

// LatestEvidence returns the latest scoped record for one task gate type.
func (r StoreEvidenceReader) LatestEvidence(ctx context.Context, projectDir, delegationID, taskID string, gateType evidence.GateType, scope inspector.EvidenceScope) (*evidence.Record, error) {
	if r.Store == nil {
		return nil, nil
	}
	readDir := inspector.EvidenceProjectDir(projectDir, scope)
	records, err := r.Store.ReadAll(ctx, readDir, delegationID, taskID, gateType)
	if err != nil {
		return nil, err
	}
	return inspector.LatestScoped(records, scope), nil
}
