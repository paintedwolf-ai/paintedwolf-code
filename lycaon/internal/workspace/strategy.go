package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sandbox"
)

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
