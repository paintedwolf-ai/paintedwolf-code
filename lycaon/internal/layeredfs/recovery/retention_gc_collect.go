package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// CollectGarbage computes the transitive closure of all referenced checkpoints, parent chains,
// content/base/candidate blobs, and private Git objects across active claims, and collects only
// unreferenced blobs, orphaned uncommitted checkpoints, and unreferenced private Git objects.
func (c *Coordinator) CollectGarbage(ctx context.Context) (*GarbageCollectionReport, error) {
	return collectGarbage(ctx, c)
}

func collectGarbage(ctx context.Context, c *Coordinator) (*GarbageCollectionReport, error) {
	refCPs, refBlobs, err := collectReferencedSets(ctx, c)
	if err != nil {
		return nil, err
	}
	report := &GarbageCollectionReport{}
	for cpID := range refCPs {
		report.RetainedCheckpoints = append(report.RetainedCheckpoints, cpID)
	}
	for sha := range refBlobs {
		report.RetainedBlobs = append(report.RetainedBlobs, sha)
	}
	slices.Sort(report.RetainedCheckpoints)
	slices.Sort(report.RetainedBlobs)

	checkpointsDir := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints")
	if entries, rErr := os.ReadDir(checkpointsDir); rErr == nil {
		for _, d := range entries {
			if !d.IsDir() {
				continue
			}
			cpID := d.Name()
			if refCPs[cpID] {
				continue
			}
			finalManifest := filepath.Join(checkpointsDir, cpID, "manifest.json")
			if _, statErr := os.Stat(finalManifest); errors.Is(statErr, fs.ErrNotExist) {
				if err := os.RemoveAll(filepath.Join(checkpointsDir, cpID)); err == nil {
					report.CollectedStagedCheckpoints = append(report.CollectedStagedCheckpoints, cpID)
				}
			}
		}
	}

	blobsDir := filepath.Join(c.cfg.CheckpointStore.RootDir(), "blobs")
	if entries, rErr := os.ReadDir(blobsDir); rErr == nil {
		for _, d := range entries {
			if d.IsDir() {
				continue
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || !refBlobs[name] {
				if err := os.Remove(filepath.Join(blobsDir, name)); err == nil {
					report.CollectedBlobs = append(report.CollectedBlobs, name)
				}
			}
		}
	}
	slices.Sort(report.CollectedBlobs)
	slices.Sort(report.CollectedStagedCheckpoints)

	if c.cfg.GitStore != nil {
		collectedGit, gErr := c.cfg.GitStore.CollectGarbage(ctx)
		if gErr != nil {
			return nil, gErr
		}
		report.CollectedGitObjects = collectedGit
	}
	return report, nil
}

func collectReferencedSets(_ context.Context, c *Coordinator) (map[string]bool, map[string]bool, error) {
	refCPs := make(map[string]bool)
	refBlobs := make(map[string]bool)

	claims, err := c.ListClaims()
	if err != nil {
		return nil, nil, err
	}
	for _, cl := range claims {
		for _, cpID := range cl.CheckpointIDs {
			refCPs[cpID] = true
		}
		for _, b := range cl.Blobs {
			if b != "" {
				refBlobs[b] = true
			}
		}
	}

	if entries, rErr := os.ReadDir(c.journalsDir); rErr == nil {
		for _, d := range entries {
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
				continue
			}
			workerID := strings.TrimSuffix(d.Name(), ".json")
			if j, jErr := c.LoadWorkerJournal(workerID); jErr == nil {
				if j.LastPublishedCheckpointID != "" {
					refCPs[j.LastPublishedCheckpointID] = true
				}
				for _, pID := range j.ParentCheckpointIDs {
					refCPs[pID] = true
				}
			}
		}
	}

	checkpointsDir := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints")
	if entries, rErr := os.ReadDir(checkpointsDir); rErr == nil {
		for _, d := range entries {
			if !d.IsDir() {
				continue
			}
			if m, mErr := c.cfg.CheckpointStore.LoadManifest(d.Name()); mErr == nil {
				for _, pID := range m.ParentCheckpoints {
					refCPs[pID] = true
				}
			}
		}
	}

	queue := make([]string, 0, len(refCPs))
	for cpID := range refCPs {
		queue = append(queue, cpID)
	}
	visited := make(map[string]bool)
	for len(queue) > 0 {
		cpID := queue[0]
		queue = queue[1:]
		if cpID == "" || visited[cpID] {
			continue
		}
		visited[cpID] = true
		refCPs[cpID] = true

		if rec, rErr := c.cfg.CheckpointStore.LoadRetentionRecord(cpID); rErr == nil {
			for _, b := range rec.Blobs {
				if b != "" {
					refBlobs[b] = true
				}
			}
		}
		m, mErr := c.cfg.CheckpointStore.LoadManifest(cpID)
		if mErr != nil {
			continue
		}
		collectManifestBlobs(m, refBlobs)
		for _, parentID := range m.ParentCheckpoints {
			if !visited[parentID] {
				queue = append(queue, parentID)
			}
		}
	}
	return refCPs, refBlobs, nil
}

func collectManifestBlobs(m checkpoint.Manifest, refBlobs map[string]bool) {
	for _, e := range m.Entries {
		if e.Final.SHA256 != "" {
			refBlobs[e.Final.SHA256] = true
		}
		if e.Base.SHA256 != "" {
			refBlobs[e.Base.SHA256] = true
		}
		for _, cb := range e.CandidateBases {
			if cb.SHA256 != "" {
				refBlobs[cb.SHA256] = true
			}
		}
	}
}

func hasUnsealedMutationsBeyondCheckpoint(backingDir string, m checkpoint.Manifest) bool {
	if strings.TrimSpace(backingDir) == "" {
		return false
	}
	byPath := make(map[layeredfs.QualifiedPath]checkpoint.ManifestEntry, len(m.Entries))
	manifestSHAs := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		byPath[e.Path] = e
		if e.Final.SHA256 != "" {
			manifestSHAs[e.Final.SHA256] = true
		}
	}

	if entries, rErr := os.ReadDir(filepath.Join(backingDir, "working")); rErr == nil {
		for _, d := range entries {
			if d.IsDir() {
				continue
			}
			raw, fErr := os.ReadFile(filepath.Join(backingDir, "working", d.Name()))
			if fErr != nil {
				return true
			}
			sum := sha256.Sum256(raw)
			sha := hex.EncodeToString(sum[:])
			if !manifestSHAs[sha] {
				return true
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(backingDir, "journal", "journal.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dirSizeBytes(filepath.Join(backingDir, "working")) > 0 && len(m.Entries) == 0
		}
		return true
	}
	snap, err := layeredfs.ParseJournalSnapshot(raw)
	if err != nil {
		return true
	}
	for _, ch := range snap.Changes {
		if !ch.LogicalChange {
			continue
		}
		e, ok := byPath[ch.Path]
		if !ok {
			return true
		}
		if ch.Deleted != e.Deleted || ch.Kind != e.Kind {
			return true
		}
		if !ch.Deleted && ch.Kind == layeredfs.EntryRegular && ch.Final.SHA256 != "" && ch.Final.SHA256 != e.Final.SHA256 {
			return true
		}
	}
	return false
}

func dirSizeBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, iErr := d.Info(); iErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func clearDirEntries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, d := range entries {
		if err := os.RemoveAll(filepath.Join(dir, d.Name())); err != nil {
			return err
		}
	}
	return nil
}
