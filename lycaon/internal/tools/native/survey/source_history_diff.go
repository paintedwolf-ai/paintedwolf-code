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

const sourceHistoryNoSessionEffectNote = "no effect recorded on this path since the session began; head is the baseline state"

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

// runSessionDiff compares head with the file's state before the first effect the
// caller's branch recorded on it this session. Both versions come from the file's
// own record, so a move within the window is not a path mismatch.
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
	head, err := ledger.Files.ResolveHead(ctx, tctx.Identity.ProjectID, branch, rootID, rel)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		resp.Note = sourceHistoryNoRecordNote
		return marshalSourceHistory(display, resp)
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
	resp.VersionID = head.VersionID
	earliest, found, err := ledger.Files.OldestFileEffectAfterOnBranch(ctx, tctx.Identity.ProjectID, branch, head.FileID, floor)
	if err != nil {
		return "", fmt.Errorf("source history session baseline: %w", err)
	}
	if !found {
		resp.BaseVersionID = head.VersionID
		resp.Note = sourceHistoryNoSessionEffectNote
		return marshalSourceHistory(display, resp)
	}
	comp, err := ledger.Comparison.CompareVersionPair(ctx, tctx.Identity.ProjectID, earliest.BeforeVersionID, head.VersionID)
	if err != nil {
		return "", comparisonError(err, head.VersionID)
	}
	if err := rejectUndiffableSides(comp); err != nil {
		return "", err
	}
	resp.BaseVersionID = comp.Before.VersionID
	fromLabel := fmt.Sprintf("%s (absent)", display)
	if comp.Before.VersionID != "" {
		fromLabel = fmt.Sprintf("%s (%s, session baseline)", display, comp.Before.VersionID)
	}
	resp.Diff = comparisonDiff(comp, fromLabel, fmt.Sprintf("%s (%s)", display, head.VersionID))
	return marshalSourceHistory(display, resp)
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
