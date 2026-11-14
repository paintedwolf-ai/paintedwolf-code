package survey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	sourceTreeBudget              = tools.WindowSettleBudget
	overviewTreeUnknownNote       = "your tree could not be compared with its baseline, so this list holds only recorded changes and other files may have changed"
	treeDiffNoTextNote            = "this file changed, but its baseline or live side has no text to compare"
	treeDiffTouchedNote           = "this file's timestamp changed; its text did not"
	treeDiffCreatedEmptyNote      = "this new file is empty"
	treeDiffDeletedEmptyNote      = "this deleted file was empty"
	treeDiffMovedNote             = "this file moved and its text did not change"
	treeDiffAbsentNote            = "neither your tree nor its baseline holds this file, so it has no change to show; source_history(path) has its record"
	treeDiffRecordUnavailableNote = "the ledger no longer holds this file's recorded bytes, so the live side names no version"
	treeDiffPastRecordNote        = "this file changed after its last recorded version, so the live side names no version"
)

func sourceTreeOf(tctx tools.ToolContext) tools.SourceTree {
	return tools.SourceTree{BaselinePath: tctx.Source.WorkerBaselinePath, BranchRoot: tctx.Source.WorkerBranchRoot, Roots: tctx.Source.Roots}
}

// addTreeChanges appends rows for what the leg's tree changed beyond the ledger's
// record. complete=false means the comparison did not finish: absence is unknown.
func (t *SourceHistoryTool) addTreeChanges(
	ctx context.Context,
	tctx tools.ToolContext,
	ledger tools.SourceHistory,
	rootBranches map[string]sourcebranch.ID,
	effects []sourceledger.Effect,
	resp *sourceHistoryResponse,
) (complete bool) {
	if ledger.Tree == nil {
		return false
	}
	compare, cancel := t.treeContext(ctx)
	defer cancel()
	changes, err := awaitTree(compare, func(ctx context.Context) ([]workspacebaseline.Change, error) {
		return ledger.Tree.TreeChanges(ctx, sourceTreeOf(tctx))
	})
	if err != nil {
		return false
	}
	roots := tctx.Source.Roots
	recorded := make(map[string]bool, len(resp.Files)+len(effects))
	for _, row := range resp.Files {
		recorded[row.Path] = true
	}
	// A rename's source is recorded too, though no row names it now. Effects
	// render in the baseline key spelling (primary-relative) so they dedupe
	// against tree changes, which carry baseline keys.
	for _, effect := range effects {
		recorded[projectroot.DisplayPath(roots, "", effect.RootID, effect.Path)] = true
		if effect.FromPath != "" {
			recorded[projectroot.DisplayPath(roots, "", effect.FromRootID, effect.FromPath)] = true
		}
	}
	covered, err := coveredByMoves(compare, ledger, sourceTreeOf(tctx), recordedMoves(roots, effects), changes, recorded)
	if err != nil {
		return false
	}
	for _, change := range changes {
		if recorded[change.Path] || covered[change.Path] {
			continue
		}
		if len(resp.Files) >= overviewEffectCap {
			resp.Truncated = true
			break
		}
		// A truncated page does not prove the ledger has no row for this path.
		if resp.Truncated {
			found, err := pathHasEffect(compare, tctx, ledger, rootBranches, change.Path)
			if err != nil {
				return false
			}
			if found {
				continue
			}
		}
		rootID, rel, locErr := displayPathLocation(roots, change.Path)
		if locErr != nil {
			return false
		}
		resp.Files = append(resp.Files, sourceHistoryOverviewFile{Path: projectroot.DisplayPath(roots, tctx.Source.ActiveRootID, rootID, rel), Op: string(changeOp(change.Kind)), Unrecorded: true})
	}
	return true
}

func (t *SourceHistoryTool) treeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := t.treeBudget
	if budget == 0 {
		budget = sourceTreeBudget
	}
	return context.WithTimeout(ctx, budget)
}

// awaitTree stops waiting when ctx ends. A filesystem call that stalled cannot be
// interrupted, so it finishes on its own and its result is dropped.
func awaitTree[T any](ctx context.Context, compare func(context.Context) (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := compare(ctx)
		done <- result{value, err}
	}()
	select {
	case got := <-done:
		return got.value, got.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func changeOp(kind workspacebaseline.ChangeKind) api.SourceChangeOp {
	switch kind {
	case workspacebaseline.ChangeCreated:
		return api.SourceChangeOpCreate
	case workspacebaseline.ChangeDeleted:
		return api.SourceChangeOpDelete
	case workspacebaseline.ChangeModified:
	}
	return api.SourceChangeOpWrite
}

func pathHasEffect(ctx context.Context, tctx tools.ToolContext, ledger tools.SourceHistory, rootBranches map[string]sourcebranch.ID, path string) (bool, error) {
	rootID, rel, err := displayPathLocation(tctx.Source.Roots, path)
	if err != nil {
		return false, err
	}
	_, found, err := ledger.Files.OldestPathEffectAfterOnBranch(ctx, tctx.Identity.ProjectID, rootBranches[rootID], rootID, rel, 0)
	return found, err
}

// displayPathLocation splits a baseline key (primary-relative display spelling)
// into its root and root-relative path.
func displayPathLocation(roots []projectroot.RootRef, path string) (rootID, rel string, err error) {
	for _, root := range roots {
		if root.IsPrimary || root.Label == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(path, "@"+root.Label+"/"); ok {
			return root.ID, rest, nil
		}
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return "", "", err
	}
	return primary.ID, path, nil
}

// workerSessionDiff diffs the baseline against the live file. A path that matches
// its baseline, or a comparison that could not run, leaves the ledger's answer. A
// ledger rejection only says the record cannot be compared; the tree still can.
func (t *SourceHistoryTool) workerSessionDiff(
	ctx context.Context,
	tctx tools.ToolContext,
	ledger tools.SourceHistory,
	display, rootID, rel string,
	recorded ledgerDiff,
	recordedErr error,
) (string, error) {
	var reject *toolrejection.ToolReject
	if recordedErr != nil && !errors.As(recordedErr, &reject) {
		return "", recordedErr
	}
	ledgerAnswer := func(note string) (string, error) {
		if recordedErr != nil {
			return "", recordedErr
		}
		return recorded.answer(ctx, tctx, display, note)
	}
	if ledger.Tree == nil {
		return ledgerAnswer(overviewTreeUnknownNote)
	}
	compare, cancel := t.treeContext(ctx)
	defer cancel()
	compared, err := awaitTree(compare, func(ctx context.Context) (workspacebaseline.PathComparison, error) {
		return ledger.Tree.TreePathChange(ctx, sourceTreeOf(tctx), projectroot.DisplayPath(tctx.Source.Roots, "", rootID, rel))
	})
	if err != nil {
		return ledgerAnswer(overviewTreeUnknownNote)
	}
	if compared.Absent && recorded.hasEffect {
		return marshalSourceHistory(display, sourceHistoryResponse{
			Mode: "diff", Path: display, Recorded: true, Since: &sourceHistorySince{}, State: "absent", Note: treeDiffAbsentNote,
		})
	}
	if compared.Kind == "" {
		return ledgerAnswer("")
	}
	branch, err := tctx.SourceBranch(rootID)
	if err != nil {
		return "", fmt.Errorf("source history branch: %w", err)
	}
	here := ledgerLocation{rootID: rootID, rel: rel}
	var beforeAt string
	movedHere, movedAway := false, false
	switch compared.Kind {
	case workspacebaseline.ChangeCreated:
		beforeAt, movedHere, err = movedBase(compare, tctx, ledger, branch, here, recorded, &compared, func(path string) (workspacebaseline.PathComparison, error) {
			return awaitTree(compare, func(ctx context.Context) (workspacebaseline.PathComparison, error) {
				return ledger.Tree.TreePathChange(ctx, sourceTreeOf(tctx), path)
			})
		})
	case workspacebaseline.ChangeDeleted:
		movedAway, err = recordedMoveAway(compare, tctx, ledger.Files, branch, here)
	case workspacebaseline.ChangeModified:
	}
	if err != nil {
		return ledgerAnswer(overviewTreeUnknownNote)
	}
	holdsRecord := recorded.hasEffect || movedHere || movedAway
	out := sourceHistoryResponse{Mode: "diff", Path: display, Recorded: holdsRecord, Unrecorded: !holdsRecord, Since: &sourceHistorySince{}}
	if compared.Kind == workspacebaseline.ChangeDeleted {
		out.State = "absent"
	}
	if !compared.HasBefore || !compared.HasAfter {
		out.Note = treeDiffNoTextNote
		return marshalSourceHistory(display, out)
	}
	// A version is named only when its bytes are the side being shown.
	if comp := recorded.comp; comp != nil && sideHolds(comp.Before, compared.Before) {
		out.BaseVersionID = comp.Before.VersionID
	}
	if recorded.hasEffect {
		comp := recorded.comp
		switch {
		case comp == nil || !sideReadable(comp.After):
			out.Note = treeDiffRecordUnavailableNote
		case comp.After.Content == compared.After:
			out.VersionID = comp.After.VersionID
		default:
			out.Note = treeDiffPastRecordNote
		}
	}
	diff, diffNote := comparisonDiff(sourceledger.Comparison{
		Before: sourceledger.ComparisonSide{VersionID: out.BaseVersionID, Content: compared.Before},
		After:  sourceledger.ComparisonSide{VersionID: out.VersionID, Content: compared.After},
	},
		treeDiffLabel(display, treeSideName("session baseline", out.BaseVersionID), compared.Kind == workspacebaseline.ChangeCreated && beforeAt == ""),
		treeDiffLabel(display, treeSideName("live", out.VersionID), compared.Kind == workspacebaseline.ChangeDeleted),
	)
	out.Diff = diff
	// Text from a credential file reaches no one until its exposure is recorded.
	if diff != "" {
		if err := deliverTreeSides(ctx, tctx, compared, beforeAt, display); err != nil {
			return "", err
		}
	}
	if diffNote == "" && diff == "" {
		diffNote = unchangedTextNote(compared.Kind, beforeAt != "")
	}
	out.Note = joinNotes(out.Note, diffNote)
	return marshalSourceHistory(display, out)
}

// unchangedTextNote says why a changed path has identical text on both sides.
func unchangedTextNote(kind workspacebaseline.ChangeKind, moved bool) string {
	switch {
	case moved:
		return treeDiffMovedNote
	case kind == workspacebaseline.ChangeCreated:
		return treeDiffCreatedEmptyNote
	case kind == workspacebaseline.ChangeDeleted:
		return treeDiffDeletedEmptyNote
	}
	return treeDiffTouchedNote
}

// deliverTreeSides records each side where its text came from: a moved file's base
// may have held its text under a credential name.
func deliverTreeSides(ctx context.Context, tctx tools.ToolContext, compared workspacebaseline.PathComparison, beforeAt, display string) error {
	beforeAbs, beforeDisplay := compared.Abs, display
	if beforeAt != "" {
		beforeAbs, beforeDisplay = beforeAt, beforeAt
	}
	if err := tctx.ObserveCredentialDelivery(ctx, beforeAbs, beforeDisplay, compared.Before); err != nil {
		return fmt.Errorf("record credential file read: %w", err)
	}
	if err := tctx.ObserveCredentialDelivery(ctx, compared.Abs, display, compared.After); err != nil {
		return fmt.Errorf("record credential file read: %w", err)
	}
	return nil
}

func sideHolds(side sourceledger.ComparisonSide, text string) bool {
	return sideReadable(side) && side.Content == text
}

func sideReadable(side sourceledger.ComparisonSide) bool {
	return side.Availability == sourceledger.ContentAvailable || side.Availability == sourceledger.ContentAbsent
}

func treeSideName(side, versionID string) string {
	if versionID == "" {
		return side
	}
	return versionID + ", " + side
}

func treeDiffLabel(display, side string, absent bool) string {
	if absent {
		return fmt.Sprintf("%s (absent)", display)
	}
	return fmt.Sprintf("%s (%s)", display, side)
}

func joinNotes(first, second string) string {
	if first == "" || second == "" {
		return first + second
	}
	return first + "; " + second
}
