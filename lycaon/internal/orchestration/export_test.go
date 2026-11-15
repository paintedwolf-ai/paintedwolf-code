package orchestration

import (
	"context"

	"github.com/lycaon/lycaon/internal/workspace"
)

// RunIDForBatchForTest observes an admitted run while synchronous Run is blocked.
func RunIDForBatchForTest(o *OrchestratorImpl, batchID string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for id, state := range o.runs {
		if state.batchID == batchID {
			return id
		}
	}
	return ""
}

// BindUnitWorkspacesIfIsolatedForTest exposes bindUnitWorkspacesIfIsolated to external tests.
func (o *OrchestratorImpl) BindUnitWorkspacesIfIsolatedForTest(
	ctx context.Context,
	spec TopologySpec,
	projectDir, batchID string,
	unitIDs []string,
) ([]*workspace.Binding, error) {
	return o.bindUnitWorkspacesIfIsolated(ctx, spec, projectDir, batchID, unitIDs)
}
