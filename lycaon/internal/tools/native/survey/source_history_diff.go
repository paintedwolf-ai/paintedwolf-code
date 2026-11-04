package survey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

const (
	sourceHistoryNoSessionEffectNote      = "no effect recorded on this path since the session began; head is the baseline state"
	sourceHistoryDeletedBeforeSessionNote = "this file was already deleted when the session began"
)

func (t *SourceHistoryTool) runDiff(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (string, error) {
	display, branch, rootID, rel, err := t.resolveHistoryPath(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	versionID, _ := args["version_id"].(string)
	versionID = strings.TrimSpace(versionID)
	baseVersionID, _ := args["base_version_id"].(string)
	baseVersionID = strings.TrimSpace(baseVersionID)
	if versionID == "" {
		if baseVersionID != "" {
			return "", &toolrejection.ToolReject{
				Code: "SOURCE_VERSION_REQUIRED",
				Data: map[string]any{
					"mode":   "diff",
					"detail": "base_version_id requires version_id",
				},
			}
		}
		return t.runSessionDiff(ctx, tctx, display, branch, rootID, rel)
	}
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}

	var comp sourceledger.Comparison
	if baseVersionID == "current" || baseVersionID == "head" {
		head, err := ledger.Files.ResolveHead(ctx, tctx.Identity.ProjectID, branch, rootID, rel)
		if err != nil {
			return "", &toolrejection.ToolReject{
				Code: "SOURCE_VERSION_NOT_FOUND",
				Data: map[string]any{
					"version_id": baseVersionID,
					"detail":     fmt.Sprintf("cannot resolve %s head for %s: %v", baseVersionID, display, err),
				},
			}
		}
		baseVersionID = head.VersionID
	}

	if baseVersionID != "" {
		comp, err = ledger.Comparison.CompareVersionPair(ctx, tctx.Identity.ProjectID, baseVersionID, versionID)
	} else {
		comp, err = ledger.Comparison.CompareVersions(ctx, tctx.Identity.ProjectID, versionID)
	}
	if err != nil {
		return "", comparisonError(err, versionID)
	}

	if (comp.After.Path != "" && comp.After.Path != rel) || (comp.Before.Path != "" && comp.Before.Path != rel) {
		expectedPath := comp.After.Path
		if expectedPath == "" {
			expectedPath = comp.Before.Path
		}
		return "", &toolrejection.ToolReject{
			Code: "SOURCE_VERSION_PATH_MISMATCH",
			Data: map[string]any{
				"version_id":    versionID,
				"path":          display,
				"expected_path": expectedPath,
				"detail":        fmt.Sprintf("version belongs to %s, not %s", expectedPath, display),
			},
		}
	}
	if err := rejectUndiffableSides(comp); err != nil {
		return "", err
	}

	fromLabel := fmt.Sprintf("%s (absent)", display)
	if comp.Before.VersionID != "" {
		fromLabel = fmt.Sprintf("%s (%s)", display, comp.Before.VersionID)
	}
	resp := sourceHistoryResponse{Mode: "diff", Path: display, Recorded: true, VersionID: versionID, BaseVersionID: comp.Before.VersionID}
	resp.Diff = comparisonDiff(comp, fromLabel, fmt.Sprintf("%s (%s)", display, versionID))
	return marshalSourceHistory(display, resp)
}

// runSessionDiff compares the file now, or its absence once deleted, with what the
// path held before the first effect the caller's branch recorded on it this
// session. Both sides come from the ledger's record, so a move within the window
// is not a path mismatch.
func (t *SourceHistoryTool) runSessionDiff(
	ctx context.Context,
	tctx tools.ToolContext,
	display string,
	branch sourcebranch.ID,
	rootID, rel string,
) (string, error) {
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}
	resp := sourceHistoryResponse{Mode: "diff", Path: display}
	current, err := resolveDiffEnd(ctx, tctx, ledger, branch, rootID, rel)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		resp.Note = sourceHistoryNoRecordNote
		return t.treeDiffOr(ctx, tctx, display, rootID, rel, resp)
	}
	if err != nil {
		return "", fmt.Errorf("source history head: %w", err)
	}
	floor, known, err := sessionWindow(ctx, tctx, ledger)
	if err != nil {
		return "", err
	}
	if !known {
		resp.Note = sourceHistoryWindowUnknownNote
		return marshalSourceHistory(display, resp)
	}
	resp.Recorded = true
	resp.Since = &sourceHistorySince{Ordinal: floor}
	resp.VersionID, resp.State = current.versionID, current.state
	earliest, found, err := ledger.Files.OldestFileEffectAfterOnBranch(ctx, tctx.Identity.ProjectID, branch, current.fileID, floor)
	if err != nil {
		return "", fmt.Errorf("source history session baseline: %w", err)
	}
	if !found {
		resp.BaseVersionID = current.versionID
		resp.Note = sourceHistoryNoSessionEffectNote
		if current.state == "absent" {
			resp.Note = sourceHistoryDeletedBeforeSessionNote
		}
		return t.treeDiffOr(ctx, tctx, display, rootID, rel, resp)
	}
	baseline := earliest.BeforeVersionID
	if baseline == "" {
		// A deleted or moved-away file leaves its record under its old identity,
		// so a new file here is not proof the path was empty at the baseline.
		atPath, found, err := ledger.Files.OldestPathEffectAfterOnBranch(ctx, tctx.Identity.ProjectID, branch, rootID, rel, floor)
		if err != nil {
			return "", fmt.Errorf("source history path baseline: %w", err)
		}
		if found {
			baseline = atPath.BeforeVersionID
		}
	}
	comp, err := ledger.Comparison.CompareVersionPair(ctx, tctx.Identity.ProjectID, baseline, current.versionID)
	if err != nil {
		return "", comparisonError(err, current.versionID)
	}
	if err := rejectUndiffableSides(comp); err != nil {
		return "", err
	}
	resp.BaseVersionID = comp.Before.VersionID
	fromLabel := fmt.Sprintf("%s (absent)", display)
	if comp.Before.VersionID != "" {
		fromLabel = fmt.Sprintf("%s (%s, session baseline)", display, comp.Before.VersionID)
	}
	toLabel := fmt.Sprintf("%s (absent)", display)
	if current.versionID != "" {
		toLabel = fmt.Sprintf("%s (%s)", display, current.versionID)
	}
	resp.Diff = comparisonDiff(comp, fromLabel, toLabel)
	return marshalSourceHistory(display, resp)
}

// diffEnd is what a session diff compares against. A deleted file has no version
// on this side: its state is absence.
type diffEnd struct {
	fileID, versionID, state string
}

// resolveDiffEnd finds the file at this path now, or the file deleted from it.
// ErrHistoryNotFound means the path was never tracked on the branch.
func resolveDiffEnd(
	ctx context.Context,
	tctx tools.ToolContext,
	ledger tools.SourceHistory,
	branch sourcebranch.ID,
	rootID, rel string,
) (diffEnd, error) {
	head, err := ledger.Files.ResolveHead(ctx, tctx.Identity.ProjectID, branch, rootID, rel)
	if err == nil {
		return diffEnd{fileID: head.FileID, versionID: head.VersionID}, nil
	}
	if !errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return diffEnd{}, err
	}
	deleted, err := ledger.Files.ResolveDeletedPath(ctx, tctx.Identity.ProjectID, branch, rootID, rel)
	if err != nil {
		return diffEnd{}, err
	}
	return diffEnd{fileID: deleted.FileID, state: "absent"}, nil
}

func comparisonError(err error, versionID string) error {
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return &toolrejection.ToolReject{
			Code: "SOURCE_VERSION_NOT_FOUND",
			Data: map[string]any{
				"version_id": versionID,
				"detail":     "one of the requested versions was not found in this project",
			},
		}
	}
	return fmt.Errorf("compare source versions: %w", err)
}

func comparisonDiff(comp sourceledger.Comparison, fromLabel, toLabel string) string {
	return sourceview.UnifiedDiff(fromLabel, toLabel, sourceview.SplitLines(comp.Before.Content), sourceview.SplitLines(comp.After.Content), 3)
}

// rejectUndiffableSides refuses a side with no text to compare, which would
// otherwise read as a diff against empty content.
func rejectUndiffableSides(comp sourceledger.Comparison) error {
	if err := rejectUndiffableSide("version", comp.After); err != nil {
		return err
	}
	return rejectUndiffableSide("base", comp.Before)
}

func rejectUndiffableSide(name string, side sourceledger.ComparisonSide) error {
	switch side.Availability {
	case sourceledger.ContentUnavailable, sourceledger.ContentNotCaptured, sourceledger.ContentBinary, sourceledger.ContentUnresolved:
		return &toolrejection.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"version_id": side.VersionID,
				"side":       name,
				"state":      string(side.Availability),
				"detail":     fmt.Sprintf("version %s has no text to diff", side.VersionID),
			},
		}
	case sourceledger.ContentAvailable, sourceledger.ContentAbsent, sourceledger.ContentDirectory:
	}
	return nil
}
