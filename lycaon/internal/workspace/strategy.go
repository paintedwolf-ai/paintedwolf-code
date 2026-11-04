package workspace

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// Adapter-backed ProvisionStrategy constants (#1658).
const (
	ProvisionMountedFUSELinux      ProvisionStrategy = ProvisionStrategy(adapter.AdapterFUSELinux)
	ProvisionMountedNFSDarwin      ProvisionStrategy = ProvisionStrategy(adapter.AdapterNFSDarwin)
	ProvisionMaterializedEagerCopy ProvisionStrategy = ProvisionStrategy(adapter.AdapterEagerCopy)
)

// WorkspaceStrategySelection is one worker's selected provider with its facts and probe result (#1658).
type WorkspaceStrategySelection struct {
	AdapterID  adapter.AdapterID      `json:"adapter_id"`
	Strategy   ProvisionStrategy      `json:"strategy"`
	Facts      adapter.AdapterFacts   `json:"facts"`
	Probe      adapter.ProbeResult    `json:"probe"`
	Selection  adapter.SelectionFacts `json:"selection"`
	CopyBudget *adapter.CopyBudget    `json:"-"`
	Adapter    adapter.Adapter        `json:"-"`
}

// DefaultWorkspaceSelector returns this host's selector. No confinement enforcer exists yet, so an
// over-budget worker finds its mounted adapter unavailable and materializes with the cost declared.
func DefaultWorkspaceSelector() *adapter.Selector {
	return platform.NewSelector(platform.Config{})
}

// StrategyForAdapterFacts derives the ProvisionStrategy from structured AdapterFacts without OS branching.
func StrategyForAdapterFacts(facts adapter.AdapterFacts) ProvisionStrategy {
	switch facts.AdapterID {
	case adapter.AdapterFUSELinux:
		return ProvisionMountedFUSELinux
	case adapter.AdapterNFSDarwin:
		return ProvisionMountedNFSDarwin
	case adapter.AdapterEagerCopy:
		return ProvisionMaterializedEagerCopy
	default:
		if facts.PreparationKind == adapter.PreparationMounted {
			return ProvisionStrategy(facts.AdapterID)
		}
		return ProvisionMaterializedEagerCopy
	}
}

// chooseWorkspaceAdapter applies the selection rule to one worker's roots. An overrun is the copy that
// just passed its budget, which moves the rule on to the mount.
func (m *Manager) chooseWorkspaceAdapter(
	ctx context.Context,
	branchRoot string,
	grants []adapter.RootGrant,
	projectID string,
	overrun *adapter.BudgetOverrunError,
) (WorkspaceStrategySelection, error) {
	selector := m.selector
	if selector == nil {
		selector = DefaultWorkspaceSelector()
	}
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	clone := adapter.ProbeCloneAt(metaDir)
	if m.cloneAvailable != nil {
		clone = m.cloneAvailable(branchRoot)
	}
	choice, err := selector.Choose(ctx, adapter.ChooseRequest{
		Probe:          adapter.ProbeConfig{RuntimeDir: filepath.Join(m.branchRoot, adapterRuntimeDirName)},
		Grants:         withIndexFacts(ctx, projectID, grants),
		CloneAvailable: clone,
		Disk: func() (adapter.DiskSpace, error) {
			available, known, err := queryAvailableStorageBytes(filepath.Dir(metaDir))
			return adapter.DiskSpace{Path: filepath.Dir(metaDir), Available: available, Known: known}, err
		},
		Overrun: overrun,
	})
	if err != nil {
		return WorkspaceStrategySelection{Probe: choice.Probe}, err
	}
	facts := choice.Adapter.Facts()
	return WorkspaceStrategySelection{
		AdapterID: choice.Adapter.ID(), Strategy: StrategyForAdapterFacts(facts), Facts: facts,
		Probe: choice.Probe, Selection: choice.Facts, CopyBudget: choice.CopyBudget, Adapter: choice.Adapter,
	}, nil
}

// withIndexFacts adds what the source catalog already knows, so a root it has fully observed costs no
// survey. A stale count is safe: the budget also binds the copy itself.
func withIndexFacts(ctx context.Context, projectID string, grants []adapter.RootGrant) []adapter.RootGrant {
	out := append([]adapter.RootGrant(nil), grants...)
	if projectID == "" {
		return out
	}
	for i := range out {
		if out[i].Estimate != nil {
			continue
		}
		root := sourcecatalog.Root{ID: out[i].RootID, Path: out[i].SourcePath}
		snapshot, settled := sourcecatalog.Process().CurrentSettled(ctx, projectID, []sourcecatalog.Root{root})
		if !settled || snapshot.State != sourcecatalog.StateReady || snapshot.RootBoundary || snapshot.Moving {
			continue
		}
		estimate := &adapter.SourceIndexEstimate{}
		for _, entry := range snapshot.Entries {
			estimate.EstimatedEntries++
			if !entry.IsDir && !entry.IsSymlink {
				estimate.EstimatedBytes += max(entry.Size, 0)
			}
		}
		out[i].Estimate = estimate
	}
	return out
}

func grantsForSources(sources []projectroot.RootRef) []adapter.RootGrant {
	grants := make([]adapter.RootGrant, 0, len(sources))
	for _, s := range sources {
		id := s.ID
		if id == "" {
			id = "primary"
		}
		label := s.Label
		if label == "" {
			label = id
		}
		grants = append(grants, adapter.RootGrant{RootID: id, Label: label, SourcePath: s.Path, IsPrimary: s.IsPrimary})
	}
	return grants
}
