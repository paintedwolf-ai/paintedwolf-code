package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

type baseComparisonUnavailable struct {
	reason string
	paths  []string
}

func (e *baseComparisonUnavailable) Error() string {
	return "scan base comparison unavailable: " + e.reason
}

// baseFindings reuses exact cache entries and scans reproducible misses.
func (r *Runner) baseFindings(ctx context.Context, job *api.CodeScan, targets []sourcesnapshot.Entry) ([]api.SecurityFinding, error) {
	fingerprint := strings.TrimSpace(job.ExecutionFingerprint)
	out := make([]api.SecurityFinding, 0)
	misses := make([]sourcesnapshot.Entry, 0)
	for _, entry := range targets {
		if fingerprint == "" || entry.ContentID() == "" {
			misses = append(misses, entry)
			continue
		}
		cached, ok, err := r.Store.BlobFindings(ctx, fingerprint, entry.ContentID(), entry.Path)
		if err != nil {
			return nil, err
		}
		if !ok {
			misses = append(misses, entry)
			continue
		}
		out = append(out, cached...)
	}
	if len(misses) == 0 {
		return out, nil
	}
	staged, err := r.stageBaseVersions(ctx, job, misses)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(staged) }()
	paths := make([]string, 0, len(misses))
	for _, entry := range misses {
		paths = append(paths, entry.Path)
	}
	result, err := r.runScanner(ctx, job, scanbase.ScanRequest{
		ProjectDir: staged, Categories: job.Categories, ScannerID: job.ScannerID,
		Paths: absoluteScanPaths(staged, paths), FileTimeout: r.FileTimeout,
	})
	if err != nil {
		return nil, err
	}
	scanoutput.NormalizeResultPaths(result, staged)
	covered := make(map[string]bool, len(result.ScannedPaths))
	for _, path := range result.ScannedPaths {
		covered[path] = true
	}
	var incomplete []string
	for _, entry := range misses {
		if !covered[entry.Path] {
			incomplete = append(incomplete, entry.Path)
		}
	}
	if len(result.Warnings) > 0 || len(incomplete) > 0 {
		return nil, &baseComparisonUnavailable{reason: "base_scan_incomplete", paths: incomplete}
	}
	out = append(out, result.Findings...)
	r.cacheFileFindings(ctx, fingerprint, misses, result)
	return out, nil
}

// stageBaseVersions never substitutes live content for an unavailable base.
func (r *Runner) stageBaseVersions(ctx context.Context, job *api.CodeScan, entries []sourcesnapshot.Entry) (dir string, err error) {
	parent := ""
	if root := r.hostDataDir(ctx, job); root != "" {
		parent = filepath.Join(root, scanbase.ScanResultSpillDir, job.ID)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", err
		}
	}
	dir, err = os.MkdirTemp(parent, "base-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	var unavailable []string
	for _, entry := range entries {
		raw, readErr := r.Snapshots.Bytes(ctx, entry)
		if ctx.Err() != nil {
			return dir, ctx.Err()
		}
		if errors.Is(readErr, sourcesnapshot.ErrContentUnavailable) {
			unavailable = append(unavailable, entry.Path)
			continue
		}
		if readErr != nil {
			return dir, readErr
		}
		dest := filepath.Join(dir, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return dir, err
		}
		if _, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: fseffect.PathLocation(dest), Source: bytes.NewReader(raw), Mode: 0o600, DirMode: 0o700,
		}); err != nil {
			return dir, err
		}
	}
	if len(unavailable) > 0 {
		sort.Strings(unavailable)
		return dir, &baseComparisonUnavailable{reason: "base_content_unavailable", paths: unavailable}
	}
	return dir, nil
}
