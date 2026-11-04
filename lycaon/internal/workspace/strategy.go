package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/viewconfine"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// CachedSeedEstimate returns a SourceIndexEstimate from a published bridge seed manifest when available (#1658).
func CachedSeedEstimate(seedRoot, sourceRoot string) *adapter.SourceIndexEstimate {
	if strings.TrimSpace(seedRoot) == "" || strings.TrimSpace(sourceRoot) == "" {
		return nil
	}
	seedDir := enginepaths.ProjectSeedDir(seedRoot, sourceRoot)
	_, tree, manifest := loadCurrentSeed(seedDir)
	if tree == "" || len(manifest.Entries) == 0 {
		return nil
	}
	return &adapter.SourceIndexEstimate{
		EstimatedEntries: int64(len(manifest.Entries)),
		EstimatedBytes:   regularBytes(manifest),
	}
}

// PrepareMaterializedSeedView prepares a bounded materialized workspace view using adapter.EagerCopyAdapter,
// populating missing root estimates from published bridge seed manifests and reporting preparation progress (#1658).
func PrepareMaterializedSeedView(
	ctx context.Context,
	seedRoot string,
	req adapter.PrepareRequest,
	fallbackReason adapter.ReasonCode,
) (*adapter.PreparedView, error) {
	grants := make([]adapter.RootGrant, len(req.Grants))
	copy(grants, req.Grants)
	for i := range grants {
		if grants[i].Estimate == nil && grants[i].Subroot == "" {
			grants[i].Estimate = CachedSeedEstimate(seedRoot, grants[i].SourcePath)
		}
	}
	req.Grants = grants
	if req.ProgressFn == nil {
		var estTotal int64
		for _, g := range grants {
			if g.Estimate != nil && g.Estimate.EstimatedBytes > 0 {
				estTotal += g.Estimate.EstimatedBytes
			}
		}
		if estTotal == 0 {
			estTotal = req.MaxBytes
		}
		req.ProgressFn = func(fp adapter.FallbackProgress) {
			reportPreparation(ctx, PreparationProgress{
				Strategy:   ProvisionMaterializedEagerCopy,
				Stage:      "materializing_seed",
				Files:      fp.ObservedEntries,
				Bytes:      fp.ObservedBytes,
				TotalBytes: estTotal,
			})
		}
	}
	adp := adapter.NewEagerCopyAdapter(fallbackReason)
	return adp.Prepare(ctx, req)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Adapter-backed ProvisionStrategy constants (#1658).
const (
	ProvisionMountedFUSELinux      ProvisionStrategy = ProvisionStrategy(adapter.AdapterFUSELinux)
	ProvisionMountedNFSDarwin      ProvisionStrategy = ProvisionStrategy(adapter.AdapterNFSDarwin)
	ProvisionMaterializedEagerCopy ProvisionStrategy = ProvisionStrategy(adapter.AdapterEagerCopy)
)

// WorkspaceStrategySelection pairs a selected workspace adapter with its structured facts and probe result (#1658).
type WorkspaceStrategySelection struct {
	AdapterID adapter.AdapterID    `json:"adapter_id"`
	Strategy  ProvisionStrategy    `json:"strategy"`
	Facts     adapter.AdapterFacts `json:"facts"`
	Probe     adapter.ProbeResult  `json:"probe"`
	Adapter   adapter.Adapter      `json:"-"`
}

// DefaultWorkspaceSelector returns this host's selector. Choosing a mount by cost is not decided yet,
// so it stays without an enforcer: its mounted adapter reports confinement_unavailable and selection
// falls back to materialized views.
func DefaultWorkspaceSelector() *adapter.Selector {
	return platform.NewSelector(platform.Config{})
}

// EnforcedWorkspaceSelector is this host's selector with its production enforcer: a mounted adapter
// passes its probe only where a confined process is actually denied a view's storage.
func EnforcedWorkspaceSelector() *adapter.Selector {
	return platform.NewSelector(platform.Config{Confinement: viewconfine.New()})
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

// SelectWorkspaceAdapter selects one workspace adapter from structured probe capabilities (#1658).
func SelectWorkspaceAdapter(
	ctx context.Context,
	probeCfg adapter.ProbeConfig,
	selector *adapter.Selector,
) (WorkspaceStrategySelection, error) {
	if selector == nil {
		selector = DefaultWorkspaceSelector()
	}
	adp, probeRes, err := selector.Select(ctx, probeCfg)
	if err != nil {
		return WorkspaceStrategySelection{}, err
	}
	facts := adp.Facts()
	return WorkspaceStrategySelection{
		AdapterID: adp.ID(),
		Strategy:  StrategyForAdapterFacts(facts),
		Facts:     facts,
		Probe:     probeRes,
		Adapter:   adp,
	}, nil
}

func chooseProvisionStrategy(ctx context.Context, m *Manager, sourceRoot, branchRoot string) (ProvisionStrategy, error) {
	if strings.TrimSpace(m.branchRoot) == "" {
		return "", fmt.Errorf("worker branch root is unavailable")
	}
	if err := os.MkdirAll(branchRoot, 0o750); err != nil {
		return "", err
	}
	sample, err := firstRegularFile(ctx, sourceRoot)
	if err != nil {
		return "", err
	}
	if sample == "" {
		return ProvisionDirectCoW, nil
	}
	direct, err := probeClone(sample, branchRoot)
	if err != nil {
		return "", fmt.Errorf("probe source-to-branch clone: %w", err)
	}
	if direct {
		return ProvisionDirectCoW, nil
	}
	if strings.TrimSpace(m.seedRoot) == "" {
		return ProvisionDirectCopy, nil
	}
	if err := os.MkdirAll(m.seedRoot, 0o700); err != nil {
		return "", err
	}
	probe, err := os.CreateTemp(m.seedRoot, ".clone-probe-*")
	if err != nil {
		return "", err
	}
	probePath := probe.Name()
	defer func() { _ = os.Remove(probePath) }()
	if _, err := probe.Write(make([]byte, 4096)); err != nil {
		_ = probe.Close()
		return "", err
	}
	if err := probe.Close(); err != nil {
		return "", err
	}
	bridged, err := probeClone(probePath, branchRoot)
	if err != nil {
		return "", fmt.Errorf("probe seed-to-branch clone: %w", err)
	}
	if bridged {
		return ProvisionBridgeCoW, nil
	}
	return ProvisionDirectCopy, nil
}

func firstRegularFile(ctx context.Context, root string) (string, error) {
	var sample string
	err := sandbox.SurveyWalk(ctx, root, sandbox.SurveyOptions{IncludeHidden: true},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			info, err := entry.DirEntry.Info()
			if err != nil {
				if sourceChangeSkippable(err) {
					return sandbox.SurveyContinue, nil
				}
				return sandbox.SurveyContinue, err
			}
			if info.Mode().IsRegular() {
				sample = entry.Abs
				return sandbox.SurveyStop, nil
			}
			return sandbox.SurveyContinue, nil
		})
	return sample, err
}

func probeClone(src, dstDir string) (bool, error) {
	dst := filepath.Join(dstDir, ".clone-probe-"+uuid.NewString())
	cloned, err := cloneWorkspaceFile(src, dst)
	removeErr := os.Remove(dst)
	if err != nil {
		return false, err
	}
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return false, removeErr
	}
	return cloned, nil
}
