package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// scanSource owns the isolated bytes consumed by one scanner execution.
type scanSource struct {
	ProjectDir string
	Paths      []string
}

func (r *Runner) resolveScanSource(ctx context.Context, job *api.CodeScan, contract scancatalog.ScannerContract, paths []string) (scanSource, bool) {
	snapshotID := strings.TrimSpace(job.SourceSnapshotID)
	if snapshotID == api.SourceSnapshotWarming {
		// A warming claim returns to the publication loop.
		r.fail(ctx, job, fmt.Errorf("scan %s source snapshot still warming", job.ID))
		return scanSource{}, false
	}
	if r.Snapshots == nil || snapshotID == "" {
		r.failTerminal(ctx, job, fmt.Errorf("scan %s has no published source snapshot", job.ID))
		return scanSource{}, false
	}
	snapshot, err := r.Snapshots.Get(ctx, snapshotID)
	if err != nil {
		r.failTerminal(ctx, job, fmt.Errorf("load source snapshot manifest: %w", err))
		return scanSource{}, false
	}
	root := snapshotRoot(snapshot, job.CanonicalPath)
	if job.TargetKind == api.ScanTargetPaths {
		targets, err := scanbase.ValidateSnapshotTargets(ctx, r.Snapshots, snapshot, paths)
		if err != nil {
			r.failWithCode(ctx, job, "SCAN_TARGET_SNAPSHOT_MISMATCH", err)
			return scanSource{}, false
		}
		files, err := snapshotFilesUnder(ctx, r.Snapshots, snapshot, root, targets)
		if err != nil {
			r.failTerminal(ctx, job, fmt.Errorf("list source snapshot targets: %w", err))
			return scanSource{}, false
		}
		return r.materializedScanSource(ctx, job, root, files, true)
	}
	if !explicitTargets(contract) {
		return r.materializedScanSource(ctx, job, root, nil, false)
	}
	files := make([]string, 0, snapshot.FileCount)
	if err := r.Snapshots.ForEachEntry(ctx, snapshot.ID, func(entry sourcesnapshot.Entry) error {
		if entry.RootPath == root {
			files = append(files, entry.Path)
		}
		return nil
	}); err != nil {
		r.failTerminal(ctx, job, fmt.Errorf("list source snapshot files: %w", err))
		return scanSource{}, false
	}
	return r.materializedScanSource(ctx, job, root, files, true)
}

func snapshotRoot(snapshot sourcesnapshot.Snapshot, canonicalPath string) string {
	if len(snapshot.Roots) == 1 {
		return snapshot.Roots[0].Path
	}
	return filepath.Clean(canonicalPath)
}

// External commands use their declared working directory; other engines receive a file list.
func explicitTargets(contract scancatalog.ScannerContract) bool {
	return contract.Driver != scancatalog.DriverExternal
}

func snapshotFilesUnder(ctx context.Context, store *sourcesnapshot.Store, snapshot sourcesnapshot.Snapshot, root string, targets []string) ([]string, error) {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		entries, err := store.EntriesUnder(ctx, snapshot.ID, root, target)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if _, dup := seen[entry.Path]; dup {
				continue
			}
			seen[entry.Path] = struct{}{}
			out = append(out, entry.Path)
		}
	}
	return out, nil
}

func absoluteScanPaths(root string, rels []string) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
	}
	return out
}

func (r *Runner) materializedScanSource(ctx context.Context, job *api.CodeScan, root string, files []string, explicit bool) (scanSource, bool) {
	dir, err := r.Snapshots.Materialize(ctx, job.SourceSnapshotID, root, files)
	if err != nil {
		r.failTerminal(ctx, job, err)
		return scanSource{}, false
	}
	source := scanSource{ProjectDir: dir}
	if explicit {
		source.Paths = absoluteScanPaths(dir, files)
	}
	return source, true
}

func (r *Runner) runScanner(ctx context.Context, job *api.CodeScan, req scanbase.ScanRequest) (*scanoutput.Result, error) {
	id := strings.TrimSpace(job.ScannerID)
	if id == "" {
		return r.Registry.RunBest(ctx, job.Categories, req)
	}
	scanner, err := r.Registry.Get(id)
	if err != nil {
		return nil, err
	}
	if scanner == nil {
		return nil, fmt.Errorf("scan runner: scanner %q not registered", id)
	}
	return scanner.Run(ctx, req)
}

func (r *Runner) ingestScanResult(ctx context.Context, job *api.CodeScan, result *scanoutput.Result, contract scancatalog.ScannerContract) bool {
	if err := r.Store.SaveSecretIdentities(ctx, job.ID, result); err != nil {
		r.failWithCode(ctx, job, scanbase.FailureIngest, err)
		return false
	}
	if r.Ingester == nil {
		return true
	}
	var touchedPaths []string
	landed, err := r.Store.LandedChangeForScan(ctx, job.ID)
	if err != nil {
		r.failWithCode(ctx, job, scanbase.FailureIngest, err)
		return false
	}
	if landed != nil {
		touchedPaths = landed.ChangedPaths
	}
	evidenceRoot := ""
	if dir := r.hostDataDir(ctx, job); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		evidenceRoot = dir
	}
	rec, err := r.Ingester.Ingest(ctx, scanbase.ScanSourceRegistry, result, scanbase.IngestMeta{
		ScanID:               job.ID,
		ProjectDir:           job.CanonicalPath,
		EvidenceRoot:         evidenceRoot,
		HeadSHA:              job.HeadSHA,
		SourceSnapshotID:     job.SourceSnapshotID,
		DelegationID:         job.DelegationID,
		Scanner:              contract,
		Categories:           job.Categories,
		TouchedPaths:         touchedPaths,
		AssessmentID:         job.AssessmentID,
		CoverageStatus:       scanoutput.CoverageForResult(result, job.SourceCaptureQuality, job.SourceAdmissionMode),
		ExecutionManifest:    job.ExecutionManifest,
		ExecutionFingerprint: job.ExecutionFingerprint,
	})
	if err != nil {
		r.fail(ctx, job, err)
		return false
	}
	if err := r.Store.SaveIngest(ctx, job.ID, rec); err != nil {
		r.failWithCode(ctx, job, scanbase.FailureIngest, err)
		return false
	}
	if err := search.ProjectScanComplete(ctx, r.Store.DB(), job.ID); err != nil {
		r.failWithCode(ctx, job, scanbase.FailureIngest, err)
		return false
	}
	return true
}
