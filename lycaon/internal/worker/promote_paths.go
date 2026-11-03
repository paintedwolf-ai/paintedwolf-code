package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func mergeComplete(assessment PromoteAssessment) bool {
	return len(assessment.CleanPaths) == 0 && len(assessment.Conflicts) == 0
}

func remainingPromoteWork(assessment PromoteAssessment, applied []string) PromoteAssessment {
	done := map[string]struct{}{}
	for _, p := range applied {
		p = strings.TrimSpace(p)
		if p != "" {
			done[p] = struct{}{}
		}
	}
	mergeByPath := map[string]PromoteMergeResult{}
	for _, mr := range assessment.MergeResults {
		mergeByPath[mr.Path] = mr
	}
	deleted := map[string]struct{}{}
	for _, p := range assessment.DeletedPaths {
		deleted[p] = struct{}{}
	}
	var out PromoteAssessment
	for _, p := range assessment.CleanPaths {
		if _, ok := done[p]; !ok {
			out.CleanPaths = append(out.CleanPaths, p)
			if snapshot, ok := assessment.primary[p]; ok {
				if out.primary == nil {
					out.primary = make(map[string]promotePrimarySnapshot)
				}
				out.primary[p] = snapshot
			}
			if _, isDel := deleted[p]; isDel {
				out.DeletedPaths = append(out.DeletedPaths, p)
			}
			if mr, ok := mergeByPath[p]; ok {
				out.MergeResults = append(out.MergeResults, mr)
			}
		}
	}
	for _, c := range assessment.Conflicts {
		if _, ok := done[c.Path]; !ok {
			out.Conflicts = append(out.Conflicts, c)
		}
	}
	return out
}

func (s *MergeService) jobPromotePaths(ctx context.Context, task *api.WorkerTask) []string {
	if s == nil || task == nil {
		return nil
	}
	return normalizeMergePaths(ctx, nil, *task, s.taskRootRefs(ctx, task))
}

func unionPromotePaths(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, raw := range group {
			p := strings.TrimSpace(raw)
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func allowedPromotePathSet(task *api.WorkerTask, roots []projectroot.RootRef, allowedRoots []string) (map[string]struct{}, error) {
	if task == nil {
		return nil, nil
	}
	promote := PromoteRootsForTask(task, roots)
	allowedFiles, err := expandOverlayPromoteFiles(promote, allowedRoots)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(allowedFiles))
	for _, p := range allowedFiles {
		allowed[p] = struct{}{}
	}
	return allowed, nil
}

// validatePromotePaths keeps requested paths present in the overlay change set.
func validatePromotePaths(task *api.WorkerTask, roots []projectroot.RootRef, allowedRoots []string, requested []string) (paths []string, err error) {
	if task == nil {
		return nil, nil
	}
	allowed, err := allowedPromotePathSet(task, roots, allowedRoots)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, raw := range requested {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if _, ok := allowed[p]; !ok {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// validatePromoteResolutionPaths keeps resolutions for changed paths.
func validatePromoteResolutionPaths(task *api.WorkerTask, roots []projectroot.RootRef, allowedRoots []string, resolutions []api.WorkerPromoteResolution) (paths []string, err error) {
	if task == nil {
		return nil, nil
	}
	allowed, err := allowedPromotePathSet(task, roots, allowedRoots)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, res := range resolutions {
		p := strings.TrimSpace(res.Path)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if _, ok := allowed[p]; !ok {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

const maxOverlayIntentSummaryRunes = 400

// EnrichOverlayIntent attaches a compact worker-intent block for preview_overlay.
func EnrichOverlayIntent(out *api.WorkerMergeResult, task *api.WorkerTask) {
	if out == nil || task == nil {
		return
	}
	intent := api.WorkerOverlayIntent{}
	if task.Result != nil {
		intent.Summary = truncateOverlayIntent(task.Result.Summary)
	}
	if task.Scope != nil {
		intent.ScopePaths = append([]string(nil), task.Scope.Normalized().Paths...)
	}
	if intent.Summary == "" {
		intent.Summary = truncateOverlayIntent(task.Brief)
	}
	if intent.Summary == "" && len(intent.ScopePaths) == 0 {
		return
	}
	out.OverlayIntent = &intent
}

func truncateOverlayIntent(s string) string {
	return runeclamp.Clamp(strings.TrimSpace(s), maxOverlayIntentSummaryRunes)
}

type promoteSyntaxIssue struct {
	path   string
	change syntaxhealth.Change
}

func validatePromoteSyntax(ctx context.Context, plans []promoteMutation) *promoteSyntaxIssue {
	for _, plan := range plans {
		if !plan.targetExists {
			continue
		}
		change := syntaxhealth.EvaluateFinal(ctx, plan.path, plan.target)
		if change.Transition != syntaxhealth.TransitionAllowed {
			return &promoteSyntaxIssue{path: plan.path, change: change}
		}
	}
	return nil
}

func (issue *promoteSyntaxIssue) rejectData(jobID string) map[string]any {
	data := issue.change.FeedbackFacts("merged")
	data["job_id"], data["path"] = jobID, issue.path
	if diag, ok := syntaxhealth.NearestDiagnostic(issue.change.After, 0, 0); ok {
		data["parse_error"] = diag.Description()
		data["syntax_diagnostic"] = diag
	}
	return data
}

type promoteFileLock struct {
	file *os.File
}

func acquirePromoteFileLock(ctx context.Context, dataDir string) (*promoteFileLock, error) {
	path := filepath.Join(dataDir, "promote.lock")
	file, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if held {
			return &promoteFileLock{file: file}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *promoteFileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = filelock.Unlock(l.file)
	_ = l.file.Close()
}

func projectConflictForSpill(c PromoteConflict) api.WorkerMergeConflict {
	return api.WorkerMergeConflict{
		Path:             c.Path,
		Reason:           c.Reason,
		Base:             c.Base,
		Primary:          c.Primary,
		Branch:           c.Branch,
		Summary:          append([]string(nil), c.Summary...),
		Hunks:            append([]api.WorkerMergeHunk(nil), c.Hunks...),
		OverlapWorkerIDs: append([]string(nil), c.OverlapJobIDs...),
		BranchDelta:      BranchDelta(c.Primary, c.Branch),
		BaseDelta:        BranchDelta(c.Base, c.Branch),
		ConflictTier:     ClassifyConflictTier(c),
	}
}

func buildPromoteSpillResult(out api.WorkerMergeResult, assessment PromoteAssessment) api.WorkerMergeResult {
	spill := out
	spill.Conflicts = nil
	for _, c := range assessment.Conflicts {
		spill.Conflicts = append(spill.Conflicts, projectConflictForSpill(c))
	}
	return spill
}

// WritePromoteSpill persists a full overlay assessment under the project host data dir.
// Returns the host-data-relative wire path (promote-spills/<id>.json) for agent read.
func WritePromoteSpill(hostDataDir, overlayID string, result api.WorkerMergeResult) (string, error) {
	hostDataDir = strings.TrimSpace(hostDataDir)
	overlayID = strings.TrimSpace(overlayID)
	if hostDataDir == "" || overlayID == "" {
		return "", nil
	}
	rel := tooloutput.PromoteSpillRelPath(overlayID)
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if tooloutput.SpillFileTooLarge(len(raw), 0) {
		return "", fmt.Errorf("promote spill exceeds %d byte cap", tooloutput.EffectiveMaxSpillFileBytes(0))
	}
	abs := filepath.Join(hostDataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: hostDataDir, Rel: filepath.FromSlash(rel)},
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o750,
	}); err != nil {
		return "", err
	}
	return rel, nil
}

func (s *MergeService) attachPromoteSpill(hostDataDir, overlayID string, out *api.WorkerMergeResult, assessment PromoteAssessment) {
	if out == nil || len(assessment.Conflicts) == 0 {
		return
	}
	rel, err := WritePromoteSpill(hostDataDir, overlayID, buildPromoteSpillResult(*out, assessment))
	if err != nil || rel == "" {
		return
	}
	out.SpillPath = rel
}
