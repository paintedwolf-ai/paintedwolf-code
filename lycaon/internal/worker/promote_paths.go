package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
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
