package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// RestoreVersionTool restores a file to a retained source version.
type RestoreVersionTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

type restoreVersionLedger interface {
	ResolveHead(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (sourceledger.BranchHead, error)
	ReadRestorableVersion(ctx context.Context, projectID, versionID string) (sourceledger.RestorableVersion, error)
	QueryFileVersions(ctx context.Context, projectID, fileID string, limit int, beforeOrdinal int64) (sourceledger.FileVersionsResult, error)
}

func restoreBranch(tctx tools.ToolContext, rootID string) sourcebranch.ID {
	if branch, err := tctx.SourceBranch(rootID); err == nil && branch != "" {
		return branch
	}
	return sourcebranch.Trunk
}

func (t *RestoreVersionTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	var overrideErr error
	ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
	if overrideErr != nil {
		return "", overrideErr
	}

	path, _ := args["path"].(string)
	versionID, _ := args["version_id"].(string)
	baseVersionID, _ := args["base_version_id"].(string)

	path = strings.TrimSpace(path)
	versionID = strings.TrimSpace(versionID)
	baseVersionID = strings.TrimSpace(baseVersionID)

	if path == "" {
		return "", toolkit.MissingArg("path")
	}

	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "restore_version"); err != nil {
		return "", writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "restore_version", err)
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return "", err
	}

	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", err
	}
	path = resolved.DisplayPath

	ledger, ok := tctx.SourceLedger.(restoreVersionLedger)
	if !ok || ledger == nil {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"path":   path,
				"detail": "the source ledger is not configured for this session",
			},
		}
	}

	rootID := resolved.Root.ID
	rel := resolved.ScopeRel
	branch := restoreBranch(tctx, rootID)

	targetVer, targetVersionID, err := resolveRestorableVersion(
		ctx, ledger, tctx.ProjectID, branch, rootID, rel, path, versionID, baseVersionID,
	)
	if err != nil {
		return "", err
	}

	if targetVer.State == "absent" {
		return restoreAbsentVersion(ctx, tctx, resolved, targetVersionID)
	}

	targetContent, ok := targetVer.Text()
	if !ok {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"path":       path,
				"version_id": targetVersionID,
				"detail":     fmt.Sprintf("exact content for version %s is unavailable", targetVersionID),
			},
		}
	}
	return t.restoreContent(ctx, tctx, resolved, targetContent, targetVersionID)
}

func resolveRestorableVersion(
	ctx context.Context,
	ledger restoreVersionLedger,
	projectID string,
	branch sourcebranch.ID,
	rootID, rel, path string,
	versionID, baseVersionID string,
) (sourceledger.RestorableVersion, string, error) {
	head, err := ledger.ResolveHead(ctx, projectID, branch, rootID, rel)
	if err != nil && !errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return sourceledger.RestorableVersion{}, "", fmt.Errorf("resolve file head: %w", err)
	}

	targetVersionID := versionID
	if targetVersionID == "" {
		if errors.Is(err, sourceledger.ErrHistoryNotFound) || head.FileID == "" {
			return sourceledger.RestorableVersion{}, "", &tools.ToolReject{
				Code: "SOURCE_VERSION_NOT_FOUND",
				Data: map[string]any{
					"path":   path,
					"detail": fmt.Sprintf("no recorded history for %s", path),
				},
			}
		}
		versionsResult, qErr := ledger.QueryFileVersions(ctx, projectID, head.FileID, 2, 0)
		if qErr != nil {
			return sourceledger.RestorableVersion{}, "", fmt.Errorf("query file versions: %w", qErr)
		}
		if len(versionsResult.Versions) < 2 {
			return sourceledger.RestorableVersion{}, "", &tools.ToolReject{
				Code: "SOURCE_VERSION_NO_PREDECESSOR",
				Data: map[string]any{
					"path":   path,
					"detail": fmt.Sprintf("file %s has only one recorded version (%s)", path, head.VersionID),
				},
			}
		}
		targetVersionID = versionsResult.Versions[1].ID
	}

	if baseVersionID != "" {
		curr := head.VersionID
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			curr = "untracked"
		}
		if curr != baseVersionID {
			return sourceledger.RestorableVersion{}, "", &tools.ToolReject{
				Code: "SOURCE_VERSION_BASE_MISMATCH",
				Data: map[string]any{
					"path":     path,
					"expected": baseVersionID,
					"current":  curr,
					"detail":   fmt.Sprintf("current version %s does not match expected base_version_id %s", curr, baseVersionID),
				},
			}
		}
	}

	targetVer, err := ledger.ReadRestorableVersion(ctx, projectID, targetVersionID)
	if err != nil {
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			return sourceledger.RestorableVersion{}, "", &tools.ToolReject{
				Code: "SOURCE_VERSION_NOT_FOUND",
				Data: map[string]any{
					"path":       path,
					"version_id": targetVersionID,
					"detail":     fmt.Sprintf("version %s was not found", targetVersionID),
				},
			}
		}
		if errors.Is(err, sourceledger.ErrVersionUnavailable) {
			return sourceledger.RestorableVersion{}, "", &tools.ToolReject{
				Code: "SOURCE_VERSION_UNAVAILABLE",
				Data: map[string]any{
					"path":       path,
					"version_id": targetVersionID,
					"detail":     fmt.Sprintf("exact content for version %s is unavailable", targetVersionID),
				},
			}
		}
		return sourceledger.RestorableVersion{}, "", fmt.Errorf("read target version %s: %w", targetVersionID, err)
	}

	if head.FileID != "" && targetVer.FileID != "" && targetVer.FileID != head.FileID {
		return sourceledger.RestorableVersion{}, "", &tools.ToolReject{
			Code: "SOURCE_VERSION_PATH_MISMATCH",
			Data: map[string]any{
				"path":       path,
				"version_id": targetVersionID,
				"detail":     fmt.Sprintf("version %s belongs to a different file", targetVersionID),
			},
		}
	}

	return targetVer, targetVersionID, nil
}

func restoreAbsentVersion(
	ctx context.Context,
	tctx tools.ToolContext,
	resolved projectpaths.Resolved,
	targetVersionID string,
) (string, error) {
	path := resolved.DisplayPath
	var before *string
	st, err := loadAgentSourceText(ctx, "restore_version", tctx, resolved)
	if err == nil {
		beforeText := st.Content
		before = &beforeText
	} else if os.IsNotExist(err) {
		return fmt.Sprintf("File %s is already absent (matches version %s)", path, targetVersionID), nil
	} else {
		return "", fmt.Errorf("read failed: %w", err)
	}

	if err := removeAgentPath(ctx, tctx, resolvedMutationTarget(resolved)); err != nil {
		return "", fmt.Errorf("restore absent state for %s: %w", path, err)
	}
	captureFileEdit(tctx, path, "", before)
	afterSuccessfulMutation(ctx, tctx, path)
	return fmt.Sprintf("Restored %s to absent state (removed file to match version %s)", path, targetVersionID), nil
}

func (t *RestoreVersionTool) restoreContent(
	ctx context.Context,
	tctx tools.ToolContext,
	resolved projectpaths.Resolved,
	targetContent string,
	targetVersionID string,
) (string, error) {
	path := resolved.DisplayPath
	return retryEditorDocument(ctx, path, func() (string, error) {
		var before *string
		st, err := loadAgentSourceText(ctx, "restore_version", tctx, resolved)
		if err == nil {
			beforeText := st.Content
			before = &beforeText
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("read failed: %w", err)
		}

		finalContent := targetContent
		reportTextIntent(tctx, resolved, st, before != nil, finalContent)
		if t.ContentApply != nil {
			var err error
			finalContent, err = t.ContentApply.GateApply(ctx, "restore_version", path, before, targetContent, tctx)
			if err != nil {
				return "", err
			}
			if finalContent != targetContent {
				reportTextIntent(tctx, resolved, st, before != nil, finalContent)
			}
		}

		landed, err := landEditedText(ctx, tctx, "restore_version", resolved, st, finalContent)
		if err != nil {
			return "", err
		}
		captureFileEdit(tctx, path, finalContent, before)
		afterSuccessfulMutation(ctx, tctx, path)
		receipt := fmt.Sprintf("Restored %s to version %s (%d bytes)", path, targetVersionID, len(finalContent))
		if note := landed.note(); note != "" {
			receipt += "\n" + note
		}
		return receipt, nil
	})
}
