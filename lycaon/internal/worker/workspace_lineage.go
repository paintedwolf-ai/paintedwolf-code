package worker

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type overlayTaskLookup interface {
	Get(jobID string) (*api.WorkerTask, bool)
}

func workspaceSourceRoots(task *api.WorkerTask, canonical []projectroot.RootRef, overlays overlayTaskLookup) ([]projectroot.RootRef, error) {
	if task == nil {
		return nil, fmt.Errorf("worker task required")
	}
	baseID := strings.TrimSpace(task.EffectiveScope().BaseOverlayID)
	if baseID == "" {
		return append([]projectroot.RootRef(nil), canonical...), nil
	}
	if overlays == nil {
		return nil, fmt.Errorf("base overlay %q is unavailable", baseID)
	}
	base, ok := overlays.Get(baseID)
	if !ok || base == nil {
		return nil, fmt.Errorf("base overlay %q is unavailable", baseID)
	}
	if base.ID == task.ID || base.ProjectID != task.ProjectID || base.ParentSessionID != task.ParentSessionID {
		return nil, fmt.Errorf("base overlay %q does not belong to this worker lineage", baseID)
	}
	switch base.MergeStatus {
	case api.WorkerMergeStatusMerged:
		return append([]projectroot.RootRef(nil), canonical...), nil
	case api.WorkerMergeStatusRejected, api.WorkerMergeStatusOrphaned, api.WorkerMergeStatusAborted:
		return nil, fmt.Errorf("base overlay %q is %s", baseID, base.MergeStatus)
	case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
	}
	if !base.EffectiveScope().IsWrite() || strings.TrimSpace(base.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("base overlay %q has no complete branch", baseID)
	}
	sources, err := workspace.BranchRootRefs(base.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("base overlay %q: %w", baseID, err)
	}
	if len(sources) != len(canonical) {
		return nil, fmt.Errorf("base overlay %q root set changed", baseID)
	}
	byID := make(map[string]projectroot.RootRef, len(sources))
	for _, source := range sources {
		byID[source.ID] = source
	}
	ordered := make([]projectroot.RootRef, 0, len(canonical))
	for _, root := range canonical {
		source, found := byID[root.ID]
		if !found {
			return nil, fmt.Errorf("base overlay %q root identity changed", baseID)
		}
		source.Label = root.Label
		source.IsPrimary = root.IsPrimary
		ordered = append(ordered, source)
	}
	return ordered, nil
}
