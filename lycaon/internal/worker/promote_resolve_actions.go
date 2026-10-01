package worker

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

type promoteMutation struct {
	path           string
	original       []byte
	originalExists bool
	originalMode   os.FileMode
	target         []byte
	targetExists   bool
}

func preparePromoteResolutions(task *api.WorkerTask, roots []projectroot.RootRef, paths []string, byPath map[string]api.WorkerPromoteResolution) ([]promoteMutation, error) {
	if task == nil {
		return nil, nil
	}
	promote := PromoteRootsForTask(task, roots)
	plans := make([]promoteMutation, 0, len(paths))
	for _, path := range paths {
		res, ok := byPath[path]
		if !ok {
			return nil, fmt.Errorf("promote resolution missing for %q", path)
		}
		plan, mutates, err := preparePromoteResolution(promote, task, res)
		if err != nil {
			return nil, err
		}
		if mutates {
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

func preparePromoteResolution(promote PromoteRoots, task *api.WorkerTask, res api.WorkerPromoteResolution) (promoteMutation, bool, error) {
	path := strings.TrimSpace(res.Path)
	if path == "" {
		return promoteMutation{}, false, nil
	}
	primaryAbs, branchAbs, err := promote.fileAbs(task, path)
	if err != nil {
		return promoteMutation{}, false, err
	}
	original, originalExists, originalMode, err := readOptionalPromoteFile(primaryAbs)
	if err != nil {
		return promoteMutation{}, false, err
	}
	plan := promoteMutation{
		path: path, original: original, originalExists: originalExists, originalMode: originalMode, targetExists: true,
	}
	action := api.WorkerPromoteResolutionAction(strings.TrimSpace(string(res.Action)))
	switch action {
	case api.WorkerPromoteResolutionActionDrop, api.WorkerPromoteResolutionActionKeepOurs:
		return promoteMutation{}, false, nil
	case api.WorkerPromoteResolutionActionKeepTheirs:
		plan.target, err = readPromoteFile(branchAbs)
	case "", api.WorkerPromoteResolutionActionKeepBoth:
		plan.target, err = resolvedPrimaryBytes(original, originalExists, path, res)
	default:
		err = fmt.Errorf("unknown promote resolution action %q for %q", action, path)
	}
	if err != nil {
		return promoteMutation{}, false, err
	}
	return plan, true, nil
}

func resolvedPrimaryBytes(original []byte, originalExists bool, path string, res api.WorkerPromoteResolution) ([]byte, error) {
	if len(res.Hunks) > 0 {
		if !originalExists {
			return nil, fmt.Errorf("hunk resolution for %q requires an existing primary file", path)
		}
		return applyHunkResolutionsToBytes(original, path, res.Hunks)
	}
	if strings.TrimSpace(res.Content) == "" {
		return nil, fmt.Errorf("keep_both for %q needs merged content or hunks; choose keep_theirs, keep_ours, or drop for an overlapping edit", path)
	}
	encoding := textfile.UTF8
	if originalExists {
		doc, _, err := textfile.Open(original, textfile.LimitsForRaw(promoteConflictMaxFileBytes))
		if err != nil {
			return nil, err
		}
		encoding = doc.Encoding()
	}
	return textfile.EncodeBounded(res.Content, encoding, textfile.LimitsForRaw(promoteConflictMaxFileBytes))
}

func applyHunkResolutionsToBytes(data []byte, path string, hunks []api.WorkerPromoteHunkResolution) ([]byte, error) {
	doc, _, err := textfile.Open(data, textfile.LimitsForRaw(promoteConflictMaxFileBytes))
	if err != nil {
		return nil, err
	}
	lines := splitMergeLines(doc.Text())
	normalized := append([]api.WorkerPromoteHunkResolution(nil), hunks...)
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].StartLine < normalized[j].StartLine
	})
	for i, h := range normalized {
		if h.StartLine < 1 || h.EndLine < h.StartLine || h.StartLine > len(lines) || h.EndLine > len(lines) {
			return nil, fmt.Errorf("hunk range %d-%d for %q is outside the %d-line primary snapshot", h.StartLine, h.EndLine, path, len(lines))
		}
		if i > 0 && h.StartLine <= normalized[i-1].EndLine {
			return nil, fmt.Errorf("hunk range %d-%d for %q overlaps %d-%d", h.StartLine, h.EndLine, path, normalized[i-1].StartLine, normalized[i-1].EndLine)
		}
	}
	for i := len(normalized) - 1; i >= 0; i-- {
		h := normalized[i]
		start := h.StartLine - 1
		end := h.EndLine
		replacement := splitMergeLines(h.Content)
		out := make([]string, 0, len(lines)-(end-start)+len(replacement))
		out = append(out, lines[:start]...)
		out = append(out, replacement...)
		out = append(out, lines[end:]...)
		lines = out
	}
	return textfile.EncodeBounded(joinMergeLines(lines), doc.Encoding(), textfile.LimitsForRaw(promoteConflictMaxFileBytes))
}

func readOptionalPromoteFile(path string) ([]byte, bool, os.FileMode, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, 0, nil
		}
		return nil, false, 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, 0, err
	}
	if info.IsDir() {
		return nil, false, 0, os.ErrNotExist
	}
	if info.Size() > promoteConflictMaxFileBytes {
		return nil, false, 0, fs.ErrPermission
	}
	raw, err := io.ReadAll(io.LimitReader(f, promoteConflictMaxFileBytes+1))
	if err != nil {
		return nil, false, 0, err
	}
	if len(raw) > promoteConflictMaxFileBytes {
		return nil, false, 0, fs.ErrPermission
	}
	return raw, true, info.Mode().Perm(), nil
}

func promoteMutationPaths(plans []promoteMutation) []string {
	paths := make([]string, 0, len(plans))
	for _, plan := range plans {
		paths = append(paths, plan.path)
	}
	return paths
}

func applyOriginalPromoteMutation(promote PromoteRoots, task *api.WorkerTask, plan promoteMutation, verify func(fseffect.Target) error) error {
	if !plan.originalExists {
		return promote.dropPrimary(task, plan.path, verify)
	}
	return promote.writePrimaryBytes(task, plan.path, plan.original, &plan.originalMode, verify)
}
