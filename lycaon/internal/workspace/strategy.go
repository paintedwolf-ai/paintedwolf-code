package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Adapter-backed ProvisionStrategy constants (#1658).
const (
	ProvisionMountedFUSELinux      ProvisionStrategy = ProvisionStrategy(adapter.AdapterFUSELinux)
	ProvisionMountedNFSDarwin      ProvisionStrategy = ProvisionStrategy(adapter.AdapterNFSDarwin)
	ProvisionMaterializedEagerCopy ProvisionStrategy = ProvisionStrategy(adapter.AdapterEagerCopy)
)

// WorkspaceStrategySelection pairs a selected workspace adapter with its structured facts and probe result (#1658).
type WorkspaceStrategySelection struct {
	AdapterID adapter.AdapterID     `json:"adapter_id"`
	Strategy  ProvisionStrategy     `json:"strategy"`
	Facts     adapter.AdapterFacts  `json:"facts"`
	Probe     adapter.ProbeResult   `json:"probe"`
	Adapter   adapter.Adapter       `json:"-"`
}

// DefaultWorkspaceSelector returns the standard platform capability selector (fuselinux, nfsdarwin, eagercopy).
func DefaultWorkspaceSelector() *adapter.Selector {
	return adapter.NewSelector(
		"",
		adapter.NewFUSELinuxAdapter(),
		adapter.NewNFSDarwinAdapter(),
		adapter.NewEagerCopyAdapter(adapter.ReasonNone),
	)
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

func (m *Manager) chooseProvisionStrategy(ctx context.Context, sourceRoot, branchRoot string) (ProvisionStrategy, error) {
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

func regularBytes(manifest seedManifest) int64 {
	var total int64
	for _, entry := range manifest.Entries {
		if entry.Kind == "regular" && entry.Size > 0 {
			total += entry.Size
		}
	}
	return total
}

func changedRegularBytes(next, current seedManifest) uint64 {
	var total uint64
	for rel, entry := range next.Entries {
		if entry.Kind != "regular" || entry.Size <= 0 {
			continue
		}
		if old, ok := current.Entries[rel]; ok && old == entry {
			continue
		}
		total += uint64(entry.Size)
	}
	return total
}

func logMirrorProgress(
	ctx context.Context,
	done <-chan struct{},
	phase, source string,
	files, bytes *atomic.Int64,
) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			slog.InfoContext(ctx, "preparing isolated worker workspace",
				"phase", phase, "source", source, "files", files.Load(), "bytes", bytes.Load())
		}
	}
}

func finalizeDirectoryMetadata(queue []directoryMetadata) error {
	for i := len(queue) - 1; i >= 0; i-- {
		entry := queue[i]
		if err := os.Chmod(entry.dst, entry.mode.Perm()); err != nil {
			return err
		}
		if err := copyExtendedMetadata(entry.src, entry.dst); err != nil {
			return err
		}
		if err := os.Chtimes(entry.dst, entry.modTime, entry.modTime); err != nil {
			return err
		}
	}
	return nil
}

func sourceChangeSkippable(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func mirrorSymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, dst); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}
