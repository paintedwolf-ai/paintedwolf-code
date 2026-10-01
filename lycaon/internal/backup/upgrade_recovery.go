package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/localdata"
)

const recoveryDescriptorName = "recovery.json"

// RecoveryPruneError reports cleanup failure after readiness is durably recorded.
type RecoveryPruneError struct{ Err error }

func (e *RecoveryPruneError) Error() string {
	return "prune completed recovery points: " + e.Err.Error()
}
func (e *RecoveryPruneError) Unwrap() error { return e.Err }

type UpgradeRecovery struct {
	Inventory        RecoveryInventory   `json:"inventory"`
	Snapshot         string              `json:"snapshot"`
	Source           migrations.Baseline `json:"source"`
	Target           migrations.Baseline `json:"target"`
	SourceAppVersion string              `json:"source_app_version"`
	TargetAppVersion string              `json:"target_app_version"`
	CreatedAt        string              `json:"created_at"`
	ReadyAt          string              `json:"ready_at,omitempty"`
}

// CaptureUpgradeRecovery runs under the exclusive store lease before any startup writers.
// Filesystem inventory keeps capture independent of the source schema.
func CaptureUpgradeRecovery(ctx context.Context, opts CreateOpts, plan migrations.Plan, targetApp string) error {
	if opts.ConfigDir == "" || opts.DBPath == "" || opts.SQLDB == nil {
		return fmt.Errorf("recovery capture requires data directory and source store")
	}
	root := filepath.Join(opts.ConfigDir, db.UpgradeRecoveryDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	pending := filepath.Join(root, "pending.json")
	if record, err := readUpgradeRecovery(pending); err == nil && record.Source == plan.Source && record.Target == plan.Target && record.SourceAppVersion == opts.AppVersion && record.TargetAppVersion == targetApp {
		if err := preflightUpgrade(ctx, opts, plan.ScratchBytes(), false); err != nil {
			return err
		}
		return verifyRecoveryDirectory(ctx, root, record)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if reused, err := reusePublishedRecovery(ctx, opts, plan, targetApp, root); reused || err != nil {
		return err
	}
	if err := preflightUpgrade(ctx, opts, plan.ScratchBytes(), true); err != nil {
		return err
	}
	name := uuid.NewString()
	temporary := ".backup-snapshot-" + name
	temporaryPath := filepath.Join(root, temporary)
	defer func() { _ = os.RemoveAll(temporaryPath) }()
	manifest, err := captureRecoveryDirectory(ctx, opts, temporaryPath)
	if err != nil {
		return err
	}
	inventory, err := manifestInventory(manifest)
	if err != nil {
		return err
	}
	record := UpgradeRecovery{Inventory: inventory, Snapshot: name, Source: plan.Source, Target: plan.Target,
		SourceAppVersion: opts.AppVersion, TargetAppVersion: targetApp, CreatedAt: manifest.CreatedAt}
	if _, _, err := inspectRecoveryPath(ctx, temporaryPath, record); err != nil {
		return err
	}
	if err := publishRecoveryDirectory(root, temporary, record); err != nil {
		return err
	}
	return publishUpgradePending(root, record)
}

func reusePublishedRecovery(ctx context.Context, opts CreateOpts, plan migrations.Plan, targetApp, root string) (bool, error) {
	records, err := upgradeRecoveryRecords(root)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record.ReadyAt != "" || record.Source != plan.Source || record.Target != plan.Target || record.SourceAppVersion != opts.AppVersion || record.TargetAppVersion != targetApp {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, record.Snapshot+".json")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return false, err
		}
		if err := preflightUpgrade(ctx, opts, plan.ScratchBytes(), false); err != nil {
			return false, err
		}
		if err := verifyRecoveryDirectory(ctx, root, record); err != nil {
			return false, err
		}
		return true, publishUpgradePending(root, record)
	}
	return false, nil
}

func publishUpgradePending(root string, record UpgradeRecovery) error {
	pending := filepath.Join(root, "pending.json")
	if previous, err := readUpgradeRecovery(pending); err == nil {
		if err := writeUpgradeRecovery(filepath.Join(root, previous.Snapshot+".json"), previous); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeUpgradeRecovery(pending, record)
}

// The descriptor moves atomically with its payload; the pending pointer is only an index.
func publishRecoveryDirectory(root, temporary string, record UpgradeRecovery) error {
	path := filepath.Join(root, temporary)
	if _, err := os.Lstat(filepath.Join(root, record.Snapshot)); err == nil {
		return fmt.Errorf("recovery destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeUpgradeRecovery(filepath.Join(path, recoveryDescriptorName), record); err != nil {
		return err
	}
	if err := syncDirectoryTree(root, path); err != nil {
		return err
	}
	if err := fseffect.Rename(root, temporary, record.Snapshot); err != nil {
		return err
	}
	return syncDirectoryTree(root, root)
}

func writeUpgradeRecovery(path string, record UpgradeRecovery) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(path), Source: strings.NewReader(string(raw)), Mode: 0o600})
	return err
}

func readUpgradeRecovery(path string) (UpgradeRecovery, error) {
	var record UpgradeRecovery
	f, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return record, err
	}
	if len(raw) > 64<<10 {
		return record, fmt.Errorf("upgrade recovery metadata exceeds limit")
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	if id, err := uuid.Parse(record.Snapshot); err != nil || id.String() != record.Snapshot {
		return record, fmt.Errorf("invalid upgrade recovery snapshot name")
	}
	return record, nil
}

func inspectRecoveryDirectory(ctx context.Context, root string, record UpgradeRecovery) (string, Manifest, error) {
	path := filepath.Join(root, record.Snapshot)
	return inspectRecoveryPath(ctx, path, record)
}

func inspectRecoveryPath(ctx context.Context, path string, record UpgradeRecovery) (string, Manifest, error) {
	inventory := record.Inventory
	if inventory.ManifestBytes <= 0 || inventory.ManifestBytes == math.MaxInt || inventory.Entries < 1 || !validSHA256(inventory.ManifestSHA256) {
		return "", Manifest{}, fmt.Errorf("invalid recovery inventory")
	}
	manifestPath, info, err := snapshotFile(path, "manifest.json")
	if err != nil {
		return "", Manifest{}, err
	}
	if info.Size() != int64(inventory.ManifestBytes) {
		return "", Manifest{}, fmt.Errorf("recovery manifest size differs")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return "", Manifest{}, err
	}
	raw, err := io.ReadAll(contextReader{ctx: ctx, in: io.LimitReader(file, int64(inventory.ManifestBytes)+1)})
	_ = file.Close()
	if err != nil {
		return "", Manifest{}, err
	}
	if len(raw) != inventory.ManifestBytes || sha256Hex(raw) != inventory.ManifestSHA256 {
		return "", Manifest{}, fmt.Errorf("recovery manifest checksum differs")
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", manifest, err
	}
	observed, err := manifestInventory(manifest)
	if err != nil || observed != inventory {
		return "", manifest, fmt.Errorf("recovery manifest inventory differs")
	}
	sizes := make(map[string]uint64, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.Size < 0 {
			return "", manifest, fmt.Errorf("invalid snapshot file size")
		}
		sizes[entry.RelPath] = uint64(entry.Size)
	}

	if err := validateManifestEntries(manifest, localdata.BackupRelPaths(), localdata.BackupRelDirs(), sizes); err != nil {
		return "", manifest, err
	}
	if manifest.SchemaUserVersion != record.Source.Revision || manifest.SchemaShapeDigest != record.Source.Shape {
		return "", manifest, fmt.Errorf("recovery schema differs from its record")
	}
	if _, err := db.PlanSchemaUpgrade(ctx, record.Source); err != nil {
		return "", manifest, err
	}
	return path, manifest, nil
}

func verifyRecoveryDirectory(ctx context.Context, root string, record UpgradeRecovery) error {
	path, manifest, err := inspectRecoveryDirectory(ctx, root, record)
	if err != nil {
		return err
	}
	for _, entry := range manifest.Files {
		if err := verifySnapshotFile(ctx, path, entry); err != nil {
			return err
		}
	}
	return nil
}

// LatestUpgradeRecovery advertises snapshots with valid metadata and a known schema route.
// Stage still verifies every payload hash before publishing a restore.
func LatestUpgradeRecovery(ctx context.Context, dataDir string) (string, UpgradeRecovery, error) {
	root := filepath.Join(dataDir, db.UpgradeRecoveryDirName)
	records, err := upgradeRecoveryRecords(root)
	if err != nil {
		return "", UpgradeRecovery{}, err
	}
	for _, record := range records {
		path, _, err := inspectRecoveryDirectory(ctx, root, record)
		if err == nil {
			return path, record, nil
		}
	}
	return "", UpgradeRecovery{}, os.ErrNotExist
}

func upgradeRecoveryRecords(root string) ([]UpgradeRecovery, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	bySnapshot := make(map[string]UpgradeRecovery)
	for _, entry := range entries {
		if entry.IsDir() {
			id, err := uuid.Parse(entry.Name())
			if err != nil || id.String() != entry.Name() {
				continue
			}
			record, err := readUpgradeRecovery(filepath.Join(root, entry.Name(), recoveryDescriptorName))
			if err == nil && record.Snapshot == entry.Name() {
				if _, exists := bySnapshot[record.Snapshot]; !exists {
					bySnapshot[record.Snapshot] = record
				}
			}
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := readUpgradeRecovery(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		bySnapshot[record.Snapshot] = record
	}
	records := make([]UpgradeRecovery, 0, len(bySnapshot))
	for _, record := range bySnapshot {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt > records[j].CreatedAt })
	return records, nil
}

// CompleteUpgradeRecovery runs only after startup has verified durable references.
// Publication before pruning preserves a recovery point across interruption.
func CompleteUpgradeRecovery(dataDir, appVersion string) error {
	root := filepath.Join(dataDir, db.UpgradeRecoveryDirName)
	pending := filepath.Join(root, "pending.json")
	record, err := readUpgradeRecovery(pending)
	if os.IsNotExist(err) {
		return completePublishedRecoveries(root)
	}
	if err != nil {
		return err
	}
	if record.TargetAppVersion == appVersion {
		record.ReadyAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := writeUpgradeRecovery(filepath.Join(root, record.Snapshot+".json"), record); err != nil {
		return err
	}
	if err := os.Remove(pending); err != nil {
		return err
	}
	if err := syncDirectoryTree(root, root); err != nil {
		return err
	}
	return completePublishedRecoveries(root)
}

// DetachUnclaimedUpgradeRecoveries prevents capture reuse after startup resumes writes.
func DetachUnclaimedUpgradeRecoveries(dataDir, appVersion string) error {
	root := filepath.Join(dataDir, db.UpgradeRecoveryDirName)
	var active string
	pendingPath := filepath.Join(root, "pending.json")
	if pending, err := readUpgradeRecovery(pendingPath); err == nil {
		if pending.TargetAppVersion == appVersion {
			active = pending.Snapshot
		} else {
			if err := writeUpgradeRecovery(filepath.Join(root, pending.Snapshot+".json"), pending); err != nil {
				return err
			}
			if err := os.Remove(pendingPath); err != nil {
				return err
			}
			if err := syncDirectoryTree(root, root); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	records, err := upgradeRecoveryRecords(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return nameUnclaimedRecoveries(root, records, active)
}

func nameUnclaimedRecoveries(root string, records []UpgradeRecovery, active string) error {
	for _, record := range records {
		if record.Snapshot == active {
			continue
		}
		path := filepath.Join(root, record.Snapshot+".json")
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := writeUpgradeRecovery(path, record); err != nil {
			return err
		}
	}
	return nil
}

func completePublishedRecoveries(root string) error {
	records, err := upgradeRecoveryRecords(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := nameUnclaimedRecoveries(root, records, ""); err != nil {
		return err
	}
	return pruneReadyRecoveries(root, records)
}

func pruneReadyRecoveries(root string, records []UpgradeRecovery) error {
	keep := make(map[string]bool)
	completed := 0
	for _, record := range records {
		if record.ReadyAt == "" || completed < 2 {
			keep[record.Snapshot] = true
			keep[record.Snapshot+".json"] = true
		}
		if record.ReadyAt != "" {
			completed++
		}
	}
	for _, record := range records {
		if keep[record.Snapshot] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, record.Snapshot)); err != nil {
			return &RecoveryPruneError{Err: err}
		}
		if err := os.Remove(filepath.Join(root, record.Snapshot+".json")); err != nil && !os.IsNotExist(err) {
			return &RecoveryPruneError{Err: err}
		}
	}

	if err := syncDirectoryTree(root, root); err != nil {
		return &RecoveryPruneError{Err: err}
	}
	return nil
}

// ValidateLiveReferences checks retained file availability before upgrade readiness.
func ValidateLiveReferences(ctx context.Context, database db.DBTX, dataDir string) error {
	branches, err := unsealedBranchTrees(ctx, database)
	if err != nil {
		return err
	}
	if err := branches.verifyPresent(dataDir); err != nil {
		return err
	}
	files := map[string]archiveSource{}
	for _, dir := range localdata.BackupRelDirs() {
		if err := collectDurableDir(ctx, dataDir, dir, files, branchCapture{all: true}, math.MaxInt); err != nil {
			return err
		}
	}
	return validateArchiveReferences(ctx, database, files)
}

// StageLatestUpgradeRecovery verifies and stages a host-created directory snapshot.
func StageLatestUpgradeRecovery(ctx context.Context, opts StageOpts) (StageResult, error) {
	if opts.ConfigDir == "" {
		return StageResult{}, fmt.Errorf("recovery restore requires data directory")
	}
	stageMu.Lock()
	defer stageMu.Unlock()
	_, record, err := LatestUpgradeRecovery(ctx, opts.ConfigDir)
	if err != nil {
		return StageResult{}, err
	}
	path, manifest, err := inspectRecoveryDirectory(ctx, filepath.Join(opts.ConfigDir, db.UpgradeRecoveryDirName), record)
	if err != nil {
		return StageResult{}, err
	}
	return stageManifest(ctx, opts, manifest, true, func(ctx context.Context, entry FileEntry, root, dest string) error {
		source, _, err := snapshotFile(path, entry.RelPath)
		if err != nil {
			return err
		}
		if err := copySnapshotRegular(ctx, source, dest, os.FileMode(entry.Mode)); err != nil {
			return err
		}
		return verifySnapshotFile(ctx, root, entry)
	})
}
