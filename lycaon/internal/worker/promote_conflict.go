package worker

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oswalk"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const promoteConflictMaxFileBytes = 2 << 20 // 2 MiB

// PromoteConflict explains why a path needs coordinator reconcile.
type PromoteConflict struct {
	Path          string `json:"path"`
	Reason        string `json:"reason"`
	Base          string `json:"base,omitempty"`
	Primary       string `json:"primary,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Hunks         []api.WorkerMergeHunk
	OverlapJobIDs []string `json:"overlap_job_ids,omitempty"`
	Summary       []string `json:"summary,omitempty"`
}

// PromoteMergeResult is a clean 3-way merge product for one path.
type PromoteMergeResult struct {
	Path     string
	Content  string
	Encoding string
}

type promotePrimarySnapshot struct {
	content []byte
	exists  bool
	mode    os.FileMode
}

// PromoteAssessment classifies every path as clean or conflicting.
type PromoteAssessment struct {
	Conflicts     []PromoteConflict `json:"conflicts,omitempty"`
	CleanPaths    []string          `json:"clean_paths,omitempty"`
	DeletedPaths  []string          `json:"deleted_paths,omitempty"`
	MergeResults  []PromoteMergeResult
	OverlapJobIDs []string           `json:"overlap_job_ids,omitempty"`
	PathOrders    []PromotePathOrder `json:"-"`
	primary       map[string]promotePrimarySnapshot
}

func expandOverlayPromoteFiles(promote PromoteRoots, roots []string) ([]string, error) {
	if len(promote.Roots) <= 1 {
		return expandOverlayPromoteFilesFlat(promote.BranchRoot, roots)
	}
	return expandOverlayPromoteFilesMultiRoot(promote, roots)
}

func expandOverlayPromoteFilesFlat(workspaceRoot string, roots []string) ([]string, error) {
	seen := map[string]struct{}{}
	var files []string
	add := func(rel string) {
		rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
		if rel == "" || rel == "." || sandbox.HasParentTraversal(rel) {
			return
		}
		if _, ok := seen[rel]; ok {
			return
		}
		seen[rel] = struct{}{}
		files = append(files, rel)
	}
	for _, raw := range roots {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		abs := filepath.Join(workspaceRoot, filepath.FromSlash(raw))
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			add(raw)
			continue
		}
		err = filepath.WalkDir(abs, func(walked string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return oswalk.Skip(walkErr)
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(workspaceRoot, walked)
			if relErr != nil {
				return oswalk.Skip(relErr)
			}
			add(filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func expandOverlayPromoteFilesMultiRoot(promote PromoteRoots, roots []string) ([]string, error) {
	primary, err := projectroot.PrimaryRoot(promote.Roots)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var files []string
	add := func(qualified string) {
		qualified = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualified)))
		if qualified == "" || qualified == "." || sandbox.HasParentTraversal(qualified) {
			return
		}
		if _, ok := seen[qualified]; ok {
			return
		}
		seen[qualified] = struct{}{}
		files = append(files, qualified)
	}
	for _, raw := range roots {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		abs := filepath.Join(promote.BranchRoot, filepath.FromSlash(raw))
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			qualifyBranchRel(promote, primary, raw, add)
			continue
		}
		err = filepath.WalkDir(abs, func(walked string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return oswalk.Skip(walkErr)
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(promote.BranchRoot, walked)
			if relErr != nil {
				return oswalk.Skip(relErr)
			}
			qualifyBranchRel(promote, primary, filepath.ToSlash(rel), add)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func qualifyBranchRel(promote PromoteRoots, primary projectroot.RootRef, branchRel string, add func(string)) {
	parts := strings.SplitN(filepath.ToSlash(branchRel), "/", 2)
	if len(parts) < 2 {
		return
	}
	branchDir, scopeRel := parts[0], parts[1]
	for _, root := range promote.Roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err == nil && dir == branchDir {
			add(projectroot.Qualify(primary, root, filepath.Join(root.Path, scopeRel)))
			return
		}
	}
}

func readPromoteFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, os.ErrNotExist
	}
	if info.Size() > promoteConflictMaxFileBytes {
		return nil, fs.ErrPermission
	}
	return os.ReadFile(path)
}

func fillMergeResult(out *api.WorkerMergeResult, assessment PromoteAssessment, opts promoteOutputOpts) {
	if out == nil {
		return
	}
	out.CleanPaths = append([]string(nil), assessment.CleanPaths...)
	for _, c := range assessment.Conflicts {
		out.Conflicts = append(out.Conflicts, projectConflictForOutput(c, opts.Detail))
	}
	out.PathStatus = buildPathStatus(assessment, out.Applied)
	out.ReadyResolutions = buildReadyResolutions(assessment)
	out.OverlapJobIDs = allOverlapJobIDs(assessment)
	if len(assessment.Conflicts) > 0 {
		out.ConflictDigest = buildConflictDigest(assessment.Conflicts)
	}
	order, promoteAfter, blockedBy, _ := aggregateOverlayPromoteOrder(assessment.PathOrders)
	if order != api.WorkerPromoteOrderIndependent || len(promoteAfter) > 0 || len(blockedBy) > 0 {
		out.PromoteOrder = order
		out.PromoteAfter = promoteAfter
		out.BlockedBy = blockedBy
	}
}

func buildConflictDigest(conflicts []PromoteConflict) []api.WorkerPromoteConflictDigest {
	if len(conflicts) == 0 {
		return nil
	}
	out := make([]api.WorkerPromoteConflictDigest, 0, len(conflicts))
	for _, c := range conflicts {
		tier := ClassifyConflictTier(c)
		branchDelta := BranchDelta(c.Primary, c.Branch)
		baseDelta := BranchDelta(c.Base, c.Branch)
		out = append(out, tooloutput.BuildConflictDigestRow(
			c.Path,
			digestSummaryForConflict(c, tier),
			c.Hunks,
			shortenDeltaForInline(branchDelta),
			shortenDeltaForInline(baseDelta),
			tier,
		))
	}
	return out
}
