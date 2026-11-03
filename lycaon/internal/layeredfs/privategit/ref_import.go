package privategit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// NamespacedRef constructs and validates a namespaced trunk ref under
// `refs/paintedwolf/workers/<job>/<checkpoint>/<ref>` (#1671).
func NamespacedRef(jobID, checkpointID, ref string) (string, error) {
	if err := ValidateRefComponent(jobID); err != nil {
		return "", fmt.Errorf("invalid jobID %q: %w", jobID, err)
	}
	if err := ValidateRefComponent(checkpointID); err != nil {
		return "", fmt.Errorf("invalid checkpointID %q: %w", checkpointID, err)
	}
	cleanRef := strings.TrimPrefix(strings.TrimSpace(ref), "refs/")
	cleanRef = strings.TrimPrefix(cleanRef, "/")
	if cleanRef == "" {
		return "", fmt.Errorf("privategit: empty ref name")
	}
	for _, seg := range strings.Split(cleanRef, "/") {
		if err := ValidateRefComponent(seg); err != nil {
			return "", fmt.Errorf("invalid ref segment %q in %q: %w", seg, ref, err)
		}
	}
	return fmt.Sprintf("refs/paintedwolf/workers/%s/%s/%s", jobID, checkpointID, cleanRef), nil
}

// ValidateRefComponent validates one path segment of a Git reference name.
func ValidateRefComponent(comp string) error {
	if comp == "" || comp == "." || comp == ".." {
		return fmt.Errorf("privategit: invalid ref component %q", comp)
	}
	if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") || strings.HasSuffix(comp, ".") {
		return fmt.Errorf("privategit: forbidden prefix/suffix in ref component %q", comp)
	}
	if strings.Contains(comp, "..") || strings.Contains(comp, "@{") {
		return fmt.Errorf("privategit: forbidden sequence in ref component %q", comp)
	}
	for _, r := range comp {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r) {
			return fmt.Errorf("privategit: illegal character %q in ref component %q", r, comp)
		}
	}
	return nil
}

// ImportRefsToTrunk performs a crash-recoverable, idempotent namespaced ref import
// of created/moved worker refs and their reachable objects into the trunk repository (#1671).
func (m *Manager) ImportRefsToTrunk(ctx context.Context, req RefImportRequest) (*RefImportResult, error) {
	return importRefsToTrunk(ctx, m, req)
}

func importRefsToTrunk(ctx context.Context, m *Manager, req RefImportRequest) (*RefImportResult, error) {
	cp := req.Checkpoint
	if cp == nil && req.CheckpointID != "" {
		m.mu.RLock()
		cp = m.checkpoints[req.CheckpointID]
		m.mu.RUnlock()
	}
	if cp == nil {
		return nil, fmt.Errorf("privategit: checkpoint state is required")
	}
	jobID := strings.TrimSpace(req.JobID)
	workerID := strings.TrimSpace(req.WorkerID)
	if jobID == "" {
		jobID = workerID
	}
	if workerID == "" {
		workerID = cp.WorkerID
	}
	if jobID == "" {
		jobID = cp.WorkerID
	}
	checkpointID := strings.TrimSpace(req.CheckpointID)
	if checkpointID == "" {
		checkpointID = cp.CheckpointID
	}
	if checkpointID != cp.CheckpointID || (req.WorkerID != "" && req.WorkerID != cp.WorkerID && req.WorkerID != cp.ViewID) {
		return nil, ErrIdentityMismatch
	}
	if strings.TrimSpace(req.TrunkDir) == "" {
		return nil, fmt.Errorf("privategit: TrunkDir is required")
	}

	planned, srcToDest, err := buildPlannedImportRefs(jobID, checkpointID, cp, req)
	if err != nil {
		return nil, err
	}

	var oids []string
	for _, oid := range planned {
		oids = append(oids, oid)
	}
	oids = append(oids, cp.Artifact.ReachableOIDs...)
	leaseID, err := m.store.RetainForRefImport(jobID+":"+checkpointID, oids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.store.ReleaseLease(leaseID) }()

	journalPath := importJournalPath(m, jobID, checkpointID)
	record, err := prepareImportJournalRecord(journalPath, jobID, workerID, checkpointID, req.TrunkDir, planned)
	if err != nil {
		return nil, err
	}
	if err := checkTrunkRefConflicts(req.TrunkDir, planned); err != nil {
		return nil, err
	}
	if err := saveImportJournalRecord(journalPath, record); err != nil {
		return nil, err
	}
	if req.CrashHook != nil {
		if err := req.CrashHook(StageIntent); err != nil {
			return nil, err
		}
	}

	trunkCommonDir := resolveTrunkCommonGitDir(req.TrunkDir)
	importedObjs, err := exportStoreObjectsToTrunk(m.store, filepath.Join(trunkCommonDir, "objects"))
	if err != nil {
		return nil, err
	}
	if err := verifyImportedTipsInTrunk(ctx, req.TrunkDir, planned); err != nil {
		return nil, err
	}

	record.Stage = StageObjectsImported
	record.ObjectsCount = importedObjs
	record.UpdatedAt = time.Now()
	if err := saveImportJournalRecord(journalPath, record); err != nil {
		return nil, err
	}
	if req.CrashHook != nil {
		if err := req.CrashHook(StageObjectsImported); err != nil {
			return nil, err
		}
	}

	destNames := make([]string, 0, len(planned))
	for d := range planned {
		destNames = append(destNames, d)
	}
	sort.Strings(destNames)

	for i, destRef := range destNames {
		wantOID := planned[destRef]
		existing := resolveRefInGitDir(trunkCommonDir, destRef)
		if existing != "" {
			if existing != wantOID {
				return nil, fmt.Errorf("%w: %s has %s, want %s", ErrRefImportConflict, destRef, existing, wantOID)
			}
		} else {
			out, code, uErr := gitexec.Run(ctx, req.TrunkDir, []string{"update-ref", destRef, wantOID, ""}, gitexec.Opts{
				Profile: gitexec.ProfileHermetic,
			})
			if uErr != nil || code != 0 {
				return nil, fmt.Errorf("%w: update-ref %s: %v (%s)", ErrRefImportConflict, destRef, uErr, strings.TrimSpace(string(out)))
			}
		}
		record.Published[destRef] = wantOID
		record.UpdatedAt = time.Now()
		_ = saveImportJournalRecord(journalPath, record)
		if i == 0 && req.CrashHook != nil {
			if err := req.CrashHook(StageMidRefPublish); err != nil {
				return nil, err
			}
		}
	}

	record.Stage = StageRefsPublished
	record.UpdatedAt = time.Now()
	if err := saveImportJournalRecord(journalPath, record); err != nil {
		return nil, err
	}
	if req.CrashHook != nil {
		if err := req.CrashHook(StageRefsPublished); err != nil {
			return nil, err
		}
	}

	return &RefImportResult{
		JobID:              jobID,
		WorkerID:           workerID,
		CheckpointID:       checkpointID,
		Stage:              StageRefsPublished,
		ImportedRefs:       planned,
		SourceToDest:       srcToDest,
		DeletedPrivateRefs: append([]string(nil), cp.DeletedRefs...),
		ObjectsImported:    importedObjs,
	}, nil
}

// LoadImportJournal reads the persisted ImportJournalRecord for (jobID, checkpointID).
func LoadImportJournal(m *Manager, jobID, checkpointID string) (ImportJournalRecord, error) {
	raw, err := os.ReadFile(importJournalPath(m, jobID, checkpointID))
	if err != nil {
		return ImportJournalRecord{}, err
	}
	var rec ImportJournalRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return ImportJournalRecord{}, err
	}
	return rec, nil
}

func importJournalPath(m *Manager, jobID, checkpointID string) string {
	safe := strings.ReplaceAll(jobID+"--"+checkpointID, "/", "_")
	return filepath.Join(m.store.importsDir, safe+".json")
}

func resolveTrunkCommonGitDir(trunkDir string) string {
	if repo, ok := gitrepo.Discover(trunkDir); ok && repo.CommonDir != "" {
		return repo.CommonDir
	}
	return filepath.Join(trunkDir, ".git")
}
