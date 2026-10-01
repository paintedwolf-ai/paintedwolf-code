package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/idset"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

type promotePathVerdict struct {
	conflict   PromoteConflict
	isConflict bool
	merged     string
	encoding   string
	// noop keeps unchanged paths visible in the merge result.
	noop bool
	// isDelete distinguishes tombstones from absent create intents.
	isDelete bool
	primary  promotePrimarySnapshot
}

func assessPromotePath3Way(ctx context.Context, promote PromoteRoots, task *api.WorkerTask, rel string, baseline *workspacebaseline.Reader) (out promotePathVerdict) {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "" {
		return promotePathVerdict{}
	}
	primaryAbs, branchAbs, err := promote.fileAbs(task, rel)
	if err != nil {
		return promotePathVerdict{isConflict: true, conflict: PromoteConflict{Path: rel, Reason: "unreadable merge inputs"}}
	}
	primaryBytes, primaryExists, primaryMode, err := readOptionalPromoteFile(primaryAbs)
	if err != nil {
		return promotePathVerdict{
			isConflict: true,
			conflict:   PromoteConflict{Path: rel, Reason: "unreadable merge inputs"},
		}
	}
	out.primary = promotePrimarySnapshot{content: primaryBytes, exists: primaryExists, mode: primaryMode}
	defer func() {
		out.primary = promotePrimarySnapshot{content: primaryBytes, exists: primaryExists, mode: primaryMode}
	}()
	branchAvailable := session.OverlayWorkspaceAvailable(task)
	var branchBytes []byte
	branchPresent := false
	if branchAvailable {
		branchBytes, err = readPromoteFile(branchAbs)
		branchPresent = err == nil
		if err != nil && !os.IsNotExist(err) {
			return promotePathVerdict{isConflict: true, conflict: PromoteConflict{Path: rel, Reason: "unreadable merge inputs"}}
		}
	}
	primaryText, primaryTextOK := openPromoteText(primaryBytes)
	if !primaryTextOK {
		return promotePathVerdict{
			isConflict: true,
			conflict:   artifactConflict(rel),
		}
	}
	branchText := promoteText{encoding: textfile.UTF8}
	if branchPresent {
		var branchTextOK bool
		branchText, branchTextOK = openPromoteText(branchBytes)
		if !branchTextOK {
			return promotePathVerdict{isConflict: true, conflict: artifactConflict(rel)}
		}
		if len(primaryBytes) > 0 && primaryText.encoding != branchText.encoding {
			return promotePathVerdict{
				isConflict: true,
				conflict:   encodingConflict(rel, primaryText, branchText),
			}
		}
	}
	verdict := assessPromotePathWithContent(ctx,
		primaryText.content, branchText.content, rel, baseline,
		branchPresent, branchAvailable,
	)
	if verdict.isConflict || verdict.noop || verdict.isDelete || !branchPresent {
		return verdict
	}
	if _, err := textfile.EncodeBounded(verdict.merged, branchText.encoding,
		textfile.LimitsForRaw(promoteConflictMaxFileBytes)); err != nil {
		return promotePathVerdict{isConflict: true, conflict: artifactConflict(rel)}
	}
	verdict.encoding = branchText.encoding
	return verdict
}

func assessPromotePathWithContent(ctx context.Context, primary, branch, rel string, baseline *workspacebaseline.Reader, branchPresent, branchAvailable bool) promotePathVerdict {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "" {
		return promotePathVerdict{}
	}
	base, existed, err := baseline.Content(ctx, rel)
	if err != nil {
		return promotePathVerdict{isConflict: true, conflict: PromoteConflict{Path: rel, Reason: "unreadable workspace baseline"}}
	}
	if !branchPresent {
		if primary == base {
			if branchAvailable && existed {
				return promotePathVerdict{isDelete: true}
			}
			return promotePathVerdict{noop: true}
		}
		return promotePathVerdict{
			isConflict: true,
			conflict: PromoteConflict{
				Path:    rel,
				Reason:  api.WorkerPromoteReasonBranchMissing,
				Base:    base,
				Primary: primary,
				Branch:  branch,
			},
		}
	}
	if primary == branch {
		return promotePathVerdict{noop: true}
	}
	merged, hunks, clean := ThreeWayMerge(base, primary, branch)
	if clean {
		return promotePathVerdict{merged: merged}
	}
	conflict := PromoteConflict{
		Path:    rel,
		Reason:  api.WorkerPromoteReasonThreeWayUnresolved,
		Base:    base,
		Primary: primary,
		Branch:  branch,
		Hunks:   hunks,
	}
	conflict.Summary = buildConflictSummary(conflict)
	return promotePathVerdict{isConflict: true, conflict: conflict}
}

func truncateMergeText(s string) string {
	const max = 8000
	if len(s) <= max {
		return s
	}
	return runeclamp.CutBytes(s, max) + "\n" + runeclamp.Marker
}

func (s *MergeService) overlapJobsForPath(ctx context.Context, sessionID, path string, skipJobID string) []string {
	if s == nil || s.Sessions == nil {
		return nil
	}
	tasks, err := s.Sessions.ListPendingOverlayPromote(ctx, sessionID)
	if err != nil {
		return nil
	}
	path = filepath.ToSlash(strings.TrimSpace(path))
	var ids []string
	for _, task := range tasks {
		if task.ID == skipJobID {
			continue
		}
		for _, root := range s.jobPromotePaths(ctx, &task) {
			promote := PromoteRootsForTask(&task, s.taskRootRefs(ctx, &task))
			files, err := expandOverlayPromoteFiles(promote, []string{root})
			if err != nil {
				continue
			}
			for _, f := range files {
				if f == path {
					ids = append(ids, task.ID)
					break
				}
			}
		}
	}
	return ids
}

func AssessPromotePaths3Way(ctx context.Context, svc *MergeService, sessionID string, task *api.WorkerTask, mergePaths []string) (PromoteAssessment, error) {
	if task == nil {
		return PromoteAssessment{}, nil
	}
	roots := []projectroot.RootRef(nil)
	if svc != nil {
		roots = svc.taskRootRefs(ctx, task)
	}
	promote := PromoteRootsForTask(task, roots)
	baseline, err := workspacebaseline.Open(ctx, task.WorkspaceBaselinePath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath))
	if err != nil {
		return PromoteAssessment{}, fmt.Errorf("worker %s has no valid workspace baseline: %w", task.ID, err)
	}
	defer func() { _ = baseline.Close() }()
	var out PromoteAssessment
	seenClean := map[string]struct{}{}
	recordSiblingOverlap := func(rel string) {
		if svc == nil {
			return
		}
		ids := svc.overlapJobsForPath(ctx, sessionID, rel, task.ID)
		out.OverlapJobIDs = idset.UnionSorted(out.OverlapJobIDs, ids)
	}
	for _, rel := range mergePaths {
		verdict := assessPromotePath3Way(ctx, promote, task, rel, baseline)
		if out.primary == nil {
			out.primary = make(map[string]promotePrimarySnapshot, len(mergePaths))
		}
		out.primary[rel] = verdict.primary
		if verdict.isDelete {
			// DeletedPaths drives primary removal.
			if _, ok := seenClean[rel]; !ok {
				seenClean[rel] = struct{}{}
				out.CleanPaths = append(out.CleanPaths, rel)
				out.DeletedPaths = append(out.DeletedPaths, rel)
				recordSiblingOverlap(rel)
			}
			continue
		}
		if verdict.noop {
			// Clean no-ops remain visible without a write.
			if _, ok := seenClean[rel]; !ok {
				seenClean[rel] = struct{}{}
				out.CleanPaths = append(out.CleanPaths, rel)
				recordSiblingOverlap(rel)
			}
			continue
		}
		if verdict.isConflict {
			conflict := verdict.conflict
			if svc != nil {
				conflict.OverlapJobIDs = svc.overlapJobsForPath(ctx, sessionID, rel, task.ID)
			}
			out.Conflicts = append(out.Conflicts, conflict)
			out.OverlapJobIDs = idset.UnionSorted(out.OverlapJobIDs, conflict.OverlapJobIDs)
			continue
		}
		if _, ok := seenClean[rel]; !ok {
			seenClean[rel] = struct{}{}
			out.CleanPaths = append(out.CleanPaths, rel)
			out.MergeResults = append(out.MergeResults, PromoteMergeResult{
				Path: rel, Content: verdict.merged, Encoding: verdict.encoding,
			})
			recordSiblingOverlap(rel)
		}
	}
	enrichAssessmentPromoteOrders(ctx, svc, sessionID, task, &out)
	return out, nil
}

type promoteText struct {
	content  string
	encoding string
}

func openPromoteText(raw []byte) (promoteText, bool) {
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(promoteConflictMaxFileBytes))
	if err != nil {
		return promoteText{}, false
	}
	return promoteText{content: doc.Text(), encoding: doc.Encoding()}, true
}

func artifactConflict(rel string) PromoteConflict {
	return PromoteConflict{
		Path:    rel,
		Reason:  api.WorkerPromoteReasonArtifact,
		Summary: []string{"binary or opaque content — host proposes keep_ours, keep_theirs, or drop"},
	}
}

func encodingConflict(rel string, primary, branch promoteText) PromoteConflict {
	return PromoteConflict{
		Path: rel, Reason: api.WorkerPromoteReasonEncodingChanged,
		Primary: primary.content, Branch: branch.content,
		Summary: []string{
			"on-disk encoding changed from " + primary.encoding + " to " + branch.encoding +
				" — choose the representation explicitly",
		},
	}
}
