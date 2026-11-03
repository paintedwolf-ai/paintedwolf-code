package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/idset"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func assessCheckpointTask(ctx context.Context, svc *MergeService, sessionID string, task *api.WorkerTask, mergePaths []string) (PromoteAssessment, bool, error) {
	var roots []projectroot.RootRef
	if svc != nil {
		roots = svc.taskRootRefs(ctx, task)
	}
	store, manifest, promoter, ok, err := session.NewTaskCheckpointPromoter(task, roots)
	if err != nil || !ok {
		return PromoteAssessment{}, ok, err
	}
	cpPreview, err := promoter.Preview(ctx, checkpoint.PreviewRequest{
		CheckpointID: manifest.CheckpointID, ExpectedWorkerID: manifest.WorkerID, ExpectedProjectID: task.ProjectID,
	})
	if err != nil {
		return PromoteAssessment{}, true, err
	}
	session.MaybeBindTaskCheckpointPreview(task.ID, cpPreview)

	wanted := make(map[string]struct{}, len(mergePaths))
	for _, p := range mergePaths {
		wanted[filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))] = struct{}{}
	}
	promote := PromoteRootsForTask(task, roots)
	out := PromoteAssessment{checkpointID: manifest.CheckpointID, primary: make(map[string]promotePrimarySnapshot, len(mergePaths))}
	for _, rel := range mergePaths {
		if primaryAbs, _, fErr := promote.fileAbs(task, rel); fErr == nil {
			b, ex, mode, _ := readOptionalPromoteFile(primaryAbs)
			out.primary[rel] = promotePrimarySnapshot{content: b, exists: ex, mode: mode}
		}
	}
	seen := make(map[string]bool, len(mergePaths))
	for _, ch := range cpPreview.PlannedChanges {
		rel := session.QualifyCheckpointPath(ch.Path, roots)
		if rel == "" {
			continue
		}
		if len(wanted) > 0 {
			if _, match := wanted[rel]; !match {
				continue
			}
		}
		seen[rel] = true
		out.CleanPaths = append(out.CleanPaths, rel)
		switch ch.Action {
		case checkpoint.ActionDelete:
			out.DeletedPaths = append(out.DeletedPaths, rel)
		case checkpoint.ActionWriteRegular, checkpoint.ActionReplaceDirWithRegular:
			out.MergeResults = append(out.MergeResults, PromoteMergeResult{Path: rel, Content: string(ch.MergedBytes), Encoding: textfile.UTF8})
		default:
		}
	}
	for _, conf := range cpPreview.Conflicts {
		rel := session.QualifyCheckpointPath(conf.Path, roots)
		if rel == "" {
			continue
		}
		if len(wanted) > 0 {
			if _, match := wanted[rel]; !match {
				continue
			}
		}
		seen[rel] = true
		pc := convertCheckpointConflict(store, out.primary[rel], conf, rel)
		if svc != nil {
			pc.OverlapJobIDs = svc.overlapJobsForPath(ctx, sessionID, rel, task.ID)
			out.OverlapJobIDs = idset.UnionSorted(out.OverlapJobIDs, pc.OverlapJobIDs)
		}
		out.Conflicts = append(out.Conflicts, pc)
	}
	for _, raw := range mergePaths {
		rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if rel != "" && !seen[rel] {
			seen[rel] = true
			out.CleanPaths = append(out.CleanPaths, rel)
		}
	}
	enrichAssessmentPromoteOrders(ctx, svc, sessionID, task, &out)
	return out, true, nil
}

func convertCheckpointConflict(store *checkpoint.Store, prim promotePrimarySnapshot, conf checkpoint.PromotionConflict, rel string) PromoteConflict {
	var baseStr, branchStr string
	if conf.Base.Kind == "regular" && conf.Base.SHA256 != "" {
		if b, err := store.ReadBlob(conf.Base.SHA256); err == nil {
			baseStr = string(b)
		}
	}
	if conf.WorkerFinal.SHA256 != "" {
		if b, err := store.ReadBlob(conf.WorkerFinal.SHA256); err == nil {
			branchStr = string(b)
		}
	}
	return PromoteConflict{
		Path: rel, Reason: api.WorkerPromoteReasonThreeWayUnresolved,
		Base: baseStr, Primary: string(prim.content), Branch: branchStr,
		Summary: []string{fmt.Sprintf("%s: %s", conf.Kind, conf.Message)},
	}
}

func commitCheckpointPromotionJournal(ctx context.Context, s *MergeService, task *api.WorkerTask) error {
	if task == nil {
		return nil
	}
	var roots []projectroot.RootRef
	if s != nil {
		roots = s.taskRootRefs(ctx, task)
	}
	return session.CommitTaskCheckpointJournal(ctx, task, roots)
}

func promoteTransactionRetainedDir(txPath string) string {
	if strings.TrimSpace(txPath) == "" {
		return ""
	}
	return strings.TrimSuffix(txPath, ".json") + ".retained"
}

func promoteRetainedSlot(relPath string) string {
	return "slot-" + contentSHA256([]byte(relPath))
}

func retainPromoteOriginals(txPath string, promote PromoteRoots, task *api.WorkerTask, plans []promoteMutation) error {
	retainedDir := promoteTransactionRetainedDir(txPath)
	if retainedDir == "" {
		return nil
	}
	_ = os.RemoveAll(retainedDir)
	for _, plan := range plans {
		if !plan.originalExists {
			continue
		}
		primaryAbs, _, err := promote.fileAbs(task, plan.path)
		if err != nil {
			return err
		}
		primaryRoot, err := promote.primaryRoot(task, primaryAbs)
		if err != nil {
			return err
		}
		primaryRel, err := filepath.Rel(primaryRoot, primaryAbs)
		if err != nil {
			return err
		}
		slot := promoteRetainedSlot(plan.path)
		if err := fseffect.HardLink(fseffect.HardLinkRequest{
			Source:   fseffect.Location{Root: primaryRoot, Rel: primaryRel},
			Location: fseffect.Location{Root: filepath.Dir(retainedDir), Rel: filepath.Join(filepath.Base(retainedDir), slot)},
			DirMode:  0o700,
			Replace:  true,
		}); err != nil {
			if errors.Is(err, syscall.EXDEV) {
				continue
			}
			return err
		}
		content, exists, mode, err := readOptionalPromoteFile(filepath.Join(retainedDir, slot))
		if err != nil {
			return err
		}
		if !promoteStateMatches(exists, mode, content, plan.originalExists, plan.originalMode, contentSHA256(plan.original)) {
			return fmt.Errorf("primary changed while promoting %q", plan.path)
		}
	}
	return nil
}

func validateRetainedPromoteOriginal(txPath string, mutation promoteJournalMutation) error {
	retainedDir := promoteTransactionRetainedDir(txPath)
	if retainedDir == "" || !mutation.OriginalExists {
		return nil
	}
	slotPath := filepath.Join(retainedDir, promoteRetainedSlot(mutation.Path))
	content, exists, mode, err := readOptionalPromoteFile(slotPath)
	if err != nil || !exists {
		return err
	}
	if !promoteStateMatches(exists, mode, content, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original)) {
		return fmt.Errorf("cannot recover promote transaction: %q original inode diverged", mutation.Path)
	}
	return nil
}

func promoteInodeMatchesRetained(txPath, primaryAbs string, mutation promoteJournalMutation) bool {
	retainedDir := promoteTransactionRetainedDir(txPath)
	if retainedDir == "" || !mutation.OriginalExists {
		return true
	}
	slotPath := filepath.Join(retainedDir, promoteRetainedSlot(mutation.Path))
	retInfo, err := os.Lstat(slotPath)
	if err != nil {
		return true
	}
	primInfo, err := os.Lstat(primaryAbs)
	if err != nil {
		return false
	}
	return os.SameFile(retInfo, primInfo)
}

func restorePromoteOriginalMutation(txPath string, promote PromoteRoots, task *api.WorkerTask, plan promoteMutation, verify func(fseffect.Target) error) error {
	retainedDir := promoteTransactionRetainedDir(txPath)
	if plan.originalExists && retainedDir != "" {
		slot := promoteRetainedSlot(plan.path)
		slotPath := filepath.Join(retainedDir, slot)
		if _, err := os.Lstat(slotPath); err == nil {
			primaryAbs, _, err := promote.fileAbs(task, plan.path)
			if err != nil {
				return err
			}
			primaryRoot, err := promote.primaryRoot(task, primaryAbs)
			if err != nil {
				return err
			}
			primaryRel, err := filepath.Rel(primaryRoot, primaryAbs)
			if err != nil {
				return err
			}
			if err := fseffect.HardLink(fseffect.HardLinkRequest{
				Source:       fseffect.Location{Root: retainedDir, Rel: slot},
				Location:     fseffect.Location{Root: primaryRoot, Rel: primaryRel},
				DirMode:      0o750,
				Replace:      true,
				BeforeCommit: verify,
			}); err != nil {
				return err
			}
			_, err = fseffect.UpdateMode(fseffect.ModeUpdateRequest{
				Location: fseffect.Location{Root: primaryRoot, Rel: primaryRel},
				Update:   func(os.FileMode) os.FileMode { return plan.originalMode.Perm() },
			})
			return err
		}
	}
	return applyOriginalPromoteMutation(promote, task, plan, verify)
}
