package execution

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// findingDelta is one scan's comparison against its base.
type findingDelta struct {
	BaseSnapshotID string
	Introduced     []api.SecurityFinding
	Fixed          []api.SecurityFinding
	Persisted      int
}

// compareWithBase keeps historical availability separate from current coverage.
func (r *Runner) compareWithBase(ctx context.Context, job *api.CodeScan, contract scancatalog.ScannerContract, result *scanoutput.Result) *findingDelta {
	if job == nil || result == nil || job.TargetKind != api.ScanTargetPaths || r.Snapshots == nil || !explicitTargets(contract) {
		return nil
	}
	baseID, err := r.Store.BaseSnapshotID(ctx, job.ID)
	if err != nil || baseID == "" {
		return nil
	}
	if len(result.Warnings) > 0 {
		r.recordUnavailableDelta(ctx, job, baseID, "current_scan_incomplete", nil)
		return nil
	}
	base, err := r.Snapshots.Get(ctx, baseID)
	if errors.Is(err, sourcesnapshot.ErrSnapshotNotFound) {
		r.recordUnavailableDelta(ctx, job, baseID, "base_snapshot_missing", nil)
		return nil
	}
	var targets []sourcesnapshot.Entry
	if err == nil {
		targets, err = baseTargets(ctx, r.Snapshots, base, job.TargetPaths, job.DeletedPaths)
	}
	var baseFindings []api.SecurityFinding
	if err == nil {
		baseFindings, err = r.baseFindings(ctx, job, targets)
	}
	if err != nil {
		var unavailable *baseComparisonUnavailable
		if errors.As(err, &unavailable) {
			r.recordUnavailableDelta(ctx, job, baseID, unavailable.reason, unavailable.paths)
		} else if ctx.Err() == nil {
			slog.WarnContext(ctx, "compare scan base", "scan_id", job.ID, "base", baseID, "error", err)
			r.recordUnavailableDelta(ctx, job, baseID, "comparison_failed", nil)
		}
		return nil
	}
	delta := diffFindings(baseFindings, result.Findings)
	delta.BaseSnapshotID = baseID
	r.recordDelta(ctx, job, api.ScanDelta{
		BaseSnapshotID: baseID, Status: "complete",
		Counts: &api.ScanDeltaCounts{Introduced: len(delta.Introduced), Fixed: len(delta.Fixed), Persisted: delta.Persisted},
	})
	return delta
}

func (r *Runner) recordUnavailableDelta(ctx context.Context, job *api.CodeScan, baseID, reason string, paths []string) {
	r.recordDelta(ctx, job, api.ScanDelta{
		BaseSnapshotID: baseID, Status: "unavailable", UnavailableReason: reason, UnavailablePaths: paths,
	})
}

func (r *Runner) recordDelta(ctx context.Context, job *api.CodeScan, delta api.ScanDelta) {
	if err := r.Store.MarkDelta(ctx, job, delta); err != nil {
		slog.WarnContext(ctx, "record scan delta", "scan_id", job.ID, "error", err)
	}
}

// baseTargets includes deleted paths retained by the base generation.
func baseTargets(ctx context.Context, store *sourcesnapshot.Store, base sourcesnapshot.Snapshot, targetPaths, deletedPaths []string) ([]sourcesnapshot.Entry, error) {
	wanted := scanbase.NormalizeScanPaths(append(append([]string(nil), targetPaths...), deletedPaths...))
	seen := make(map[string]struct{})
	out := make([]sourcesnapshot.Entry, 0, len(wanted))
	for _, root := range base.Roots {
		for _, target := range wanted {
			entries, err := store.EntriesUnder(ctx, base.ID, root.Path, target)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				key := entry.RootPath + "\x00" + entry.Path
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, entry)
			}
		}
	}
	return out, nil
}

// cacheCurrentFindings retains results while their verified source versions exist.
func (r *Runner) cacheCurrentFindings(ctx context.Context, job *api.CodeScan, contract scancatalog.ScannerContract, result *scanoutput.Result) {
	if r.Snapshots == nil || !explicitTargets(contract) || job.ExecutionFingerprint == "" || job.SourceCaptureQuality != string(sourcesnapshot.CaptureExact) {
		return
	}
	if err := r.cacheSnapshotFindings(ctx, job, fileFindingCache(result)); err != nil {
		slog.WarnContext(ctx, "cache current scan findings", "scan_id", job.ID, "error", err)
	}
}

func (r *Runner) cacheSnapshotFindings(ctx context.Context, job *api.CodeScan, cached map[string][]api.SecurityFinding) error {
	if len(cached) == 0 {
		return nil
	}
	snapshot, err := r.Snapshots.Get(ctx, job.SourceSnapshotID)
	if err != nil {
		return err
	}
	root := snapshotRoot(snapshot, job.CanonicalPath)
	writer := fileFindingCacheWriter{runner: r, fingerprint: job.ExecutionFingerprint, cached: cached}
	// A one-file delta reads one manifest entry, even in a repository with millions.
	for rel := range cached {
		entry, found, err := r.Snapshots.Lookup(ctx, job.SourceSnapshotID, root, rel)
		if err != nil {
			return err
		}
		if found {
			if err := writer.add(ctx, entry); err != nil {
				return err
			}
		}
	}
	return writer.flush(ctx)
}

func cacheableResultPaths(result *scanoutput.Result) map[string]bool {
	eligible := make(map[string]bool)
	if result == nil {
		return eligible
	}
	for _, rel := range result.ScannedPaths {
		eligible[rel] = true
	}
	for _, warning := range result.Warnings {
		if warning.File == "" {
			return nil
		}
		delete(eligible, warning.File)
	}
	for _, finding := range result.Findings {
		if _, single := singleFile(finding); single {
			continue
		}
		// Every participating file is incomplete in a per-file cache.
		scanfindings.VisitFindingLocations(&finding, func(location *api.SecurityFindingLocation) {
			delete(eligible, strings.Trim(location.URI, "/"))
		})
	}
	return eligible
}

func fileFindingCache(result *scanoutput.Result) map[string][]api.SecurityFinding {
	eligible := cacheableResultPaths(result)
	byFile := make(map[string][]api.SecurityFinding, len(eligible))
	for rel := range eligible {
		byFile[rel] = nil
	}
	if result != nil {
		for _, finding := range result.Findings {
			file, single := singleFile(finding)
			if single && eligible[file] {
				byFile[file] = append(byFile[file], finding)
			}
		}
	}
	return byFile
}

// cacheFileFindings never promotes unscanned, moved, or partially covered files.
func (r *Runner) cacheFileFindings(ctx context.Context, fingerprint string, scanned []sourcesnapshot.Entry, result *scanoutput.Result) {
	writer := fileFindingCacheWriter{runner: r, fingerprint: fingerprint, cached: fileFindingCache(result)}
	for _, entry := range scanned {
		if err := writer.add(ctx, entry); err != nil {
			slog.WarnContext(ctx, "cache scan findings", "error", err)
			return
		}
	}
	if err := writer.flush(ctx); err != nil {
		slog.WarnContext(ctx, "cache scan findings", "error", err)
	}
}

type fileFindingCacheWriter struct {
	runner      *Runner
	fingerprint string
	cached      map[string][]api.SecurityFinding
	pending     []scanbase.CachedFileResult
}

func (w *fileFindingCacheWriter) add(ctx context.Context, entry sourcesnapshot.Entry) error {
	findings, eligible := w.cached[entry.Path]
	if w.fingerprint == "" || entry.ContentID() == "" || !eligible {
		return ctx.Err()
	}
	w.pending = append(w.pending, scanbase.CachedFileResult{ContentID: entry.ContentID(), Path: entry.Path, Findings: findings})
	if len(w.pending) >= 256 {
		return w.flush(ctx)
	}
	return ctx.Err()
}

func (w *fileFindingCacheWriter) flush(ctx context.Context) error {
	if len(w.pending) == 0 {
		return ctx.Err()
	}
	err := w.runner.Store.SaveBlobFindings(ctx, w.fingerprint, w.pending, time.Now().UTC())
	w.pending = w.pending[:0]
	return err
}

// singleFile reports the one file a finding's locations name, or the first
// file and false when they span more than one.
func singleFile(finding api.SecurityFinding) (string, bool) {
	file := ""
	single := true
	scanfindings.VisitFindingLocations(&finding, func(location *api.SecurityFindingLocation) {
		uri := strings.Trim(location.URI, "/")
		if file == "" {
			file = uri
		} else if uri != file {
			single = false
		}
	})
	return file, single
}

// diffFindings matches identity first, then rule, file, and message to absorb line movement.
func diffFindings(base, current []api.SecurityFinding) *findingDelta {
	out := &findingDelta{}
	baseByID := make(map[string]int, len(base))
	for i, finding := range base {
		baseByID[scanbase.FindingIdentity(finding)] = i
	}
	matchedBase := make(map[int]bool, len(base))
	unmatched := make([]api.SecurityFinding, 0)
	for _, finding := range current {
		if i, ok := baseByID[scanbase.FindingIdentity(finding)]; ok && !matchedBase[i] {
			matchedBase[i] = true
			out.Persisted++
			continue
		}
		unmatched = append(unmatched, finding)
	}
	baseByShape := make(map[string][]int, len(base))
	for i, finding := range base {
		if !matchedBase[i] {
			key := findingShape(finding)
			baseByShape[key] = append(baseByShape[key], i)
		}
	}
	for _, finding := range unmatched {
		key := findingShape(finding)
		if candidates := baseByShape[key]; len(candidates) > 0 {
			matchedBase[candidates[0]] = true
			baseByShape[key] = candidates[1:]
			out.Persisted++
			continue
		}
		out.Introduced = append(out.Introduced, finding)
	}
	for i, finding := range base {
		if !matchedBase[i] {
			out.Fixed = append(out.Fixed, finding)
		}
	}
	return out
}

// findingShape is a finding without its line: rule, file, and message.
func findingShape(finding api.SecurityFinding) string {
	file, _ := singleFile(finding)
	return strings.Join([]string{strings.TrimSpace(finding.RuleID), file, strings.TrimSpace(finding.Message)}, "\x00")
}

// recordFindingHistory records ledger transitions for completed scans.
func (r *Runner) recordFindingHistory(ctx context.Context, job *api.CodeScan) {
	if job == nil || job.Status != api.CodeScanStatusComplete || strings.TrimSpace(job.ScannerID) == "" {
		return
	}
	if err := r.Store.InvalidateIgnoreDigest(ctx, job.CanonicalPath); err != nil {
		slog.WarnContext(ctx, "invalidate scan decisions", "scan_id", job.ID, "error", err)
	}
	introduced, fixed, ok := r.findingHistoryDelta(ctx, job)
	if !ok {
		return
	}
	if err := r.Store.RecordFindingEvents(ctx, job, introduced, fixed, time.Now().UTC()); err != nil {
		slog.WarnContext(ctx, "record finding history", "scan_id", job.ID, "error", err)
	}
	if r.OnDelta != nil && (len(introduced) > 0 || len(fixed) > 0) {
		r.OnDelta(ctx, *job, introduced, fixed)
	}
}

// Ledger presence determines transitions independently of snapshot reuse.
func (r *Runner) findingHistoryDelta(ctx context.Context, job *api.CodeScan) (introduced, fixed []api.SecurityFinding, ok bool) {
	open, err := r.Store.OpenFindings(ctx, job.CanonicalPath, job.ScannerID)
	if err != nil {
		slog.WarnContext(ctx, "read open findings", "scan_id", job.ID, "error", err)
		return nil, nil, false
	}
	scope := findingHistoryScope(job)
	introduced, fixed = diffAgainstOpen(open, job.Findings, scope)
	if job.TargetKind != api.ScanTargetFull && len(scope) == 0 {
		return introduced, nil, true
	}
	unconfirmed, err := r.Store.FindingsNeedingConfirmation(ctx, job)
	if err != nil {
		slog.WarnContext(ctx, "read unconfirmed findings", "scan_id", job.ID, "error", err)
		return nil, nil, false
	}
	_, confirmed := diffAgainstOpen(unconfirmed, job.Findings, scope)
	fixed = append(fixed, confirmed...)
	scanbase.SortFindingsByIdentity(fixed)
	return introduced, fixed, true
}

// findingHistoryScope limits absence confirmation to scanned and deleted paths.
func findingHistoryScope(job *api.CodeScan) []string {
	if job.TargetKind == api.ScanTargetFull {
		return nil
	}
	return scanbase.NormalizeScanPaths(append(append([]string(nil), job.TargetPaths...), job.DeletedPaths...))
}

// diffAgainstOpen marks missing findings fixed only within the scanned scope.
func diffAgainstOpen(open map[string]api.SecurityFinding, current []api.SecurityFinding, scope []string) (introduced, fixed []api.SecurityFinding) {
	seen := make(map[string]bool, len(current))
	introduced = make([]api.SecurityFinding, 0, len(current))
	for _, finding := range current {
		id := scanbase.FindingIdentity(finding)
		seen[id] = true
		if _, held := open[id]; !held {
			introduced = append(introduced, finding)
		}
	}
	fixed = make([]api.SecurityFinding, 0)
	for id, finding := range open {
		if seen[id] {
			continue
		}
		if len(scope) > 0 && !scanbase.FindingTouchesPaths(finding, scope) {
			continue
		}
		fixed = append(fixed, finding)
	}
	scanbase.SortFindingsByIdentity(fixed)
	return introduced, fixed
}
