package survey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// SourceHistoryTool reads recorded file changes and line authorship.
type SourceHistoryTool struct {
	Boundary *sandbox.Boundary
}

type sourceHistoryLedger interface {
	sourceview.Provenance
	QueryAttribution(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (sourceledger.AttributionResult, error)
	SessionAuthoredPaths(ctx context.Context, projectID, sessionID, rootID string) ([]string, error)
	GitTransitionsByIDs(ctx context.Context, ids []string) (map[string]sourceledger.GitTransition, error)
	ReadRestorableVersion(ctx context.Context, projectID, versionID string) (sourceledger.RestorableVersion, error)
	CompareVersions(ctx context.Context, projectID, versionID string) (sourceledger.Comparison, error)
	CompareVersionPair(ctx context.Context, projectID, beforeID, afterID string) (sourceledger.Comparison, error)
}

const (
	sourceHistoryDefaultLimit = 20
	sourceHistoryMaxLimit     = 100
	sourceHistoryNoRecordNote = "no recorded history addressable at this path — never tracked here, or currently absent; treat provenance as unknown, not as unchanged"
)

type sourceHistoryEffect struct {
	Actor     string            `json:"actor"`
	Detail    string            `json:"detail,omitempty"`
	Op        string            `json:"op"`
	At        string            `json:"at"`
	Turn      int               `json:"turn,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	Cause     string            `json:"cause,omitempty"`
	Git       *sourceHistoryGit `json:"git,omitempty"`
	VersionID string            `json:"version_id,omitempty"`
	Ordinal   int64             `json:"ordinal"`
	FromPath  string            `json:"from_path,omitempty"`

	// gitTransitionID resolves after pagination and stays out of the response.
	gitTransitionID string
}

// sourceHistoryGit is the recorded ref movement behind an external effect.
type sourceHistoryGit struct {
	Kind       string `json:"kind"`
	FromRef    string `json:"from_ref,omitempty"`
	ToRef      string `json:"to_ref,omitempty"`
	FromCommit string `json:"from_commit,omitempty"`
	ToCommit   string `json:"to_commit,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type sourceHistoryTip struct {
	State       string `json:"state"`
	SHA256Short string `json:"sha256_12,omitempty"`
	VersionID   string `json:"version_id,omitempty"`
}

type sourceHistoryInterval struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Actor     string `json:"actor"`
	Detail    string `json:"detail,omitempty"`
	Turn      int    `json:"turn,omitempty"`
	At        string `json:"at"`
}

type sourceHistoryResponse struct {
	Mode              string                  `json:"mode"`
	Path              string                  `json:"path,omitempty"`
	Recorded          bool                    `json:"recorded"`
	Tip               *sourceHistoryTip       `json:"tip,omitempty"`
	Effects           []sourceHistoryEffect   `json:"effects,omitempty"`
	NextBeforeOrdinal int64                   `json:"next_before_ordinal,omitempty"`
	HeadSHA256Short   string                  `json:"head_sha256_12,omitempty"`
	Intervals         []sourceHistoryInterval `json:"intervals,omitempty"`
	Paths             []string                `json:"paths,omitempty"`
	Count             int                     `json:"count,omitempty"`
	Note              string                  `json:"note,omitempty"`

	// version / diff modes
	VersionID     string `json:"version_id,omitempty"`
	BaseVersionID string `json:"base_version_id,omitempty"`
	State         string `json:"state,omitempty"`
	Content       string `json:"content,omitempty"`
	TotalLines    int    `json:"total_lines,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	NextOffset    *int   `json:"next_offset,omitempty"`
	Diff          string `json:"diff,omitempty"`
}

func (t *SourceHistoryTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	mode := "effects"
	if raw, has := args["mode"].(string); has && strings.TrimSpace(raw) != "" {
		mode = strings.TrimSpace(raw)
	}
	switch mode {
	case "effects":
		return t.runEffects(ctx, args, tctx)
	case "lines":
		return t.runLines(ctx, args, tctx)
	case "mine":
		return t.runMine(ctx, tctx)
	case "version":
		return t.runVersion(ctx, args, tctx)
	case "diff":
		return t.runDiff(ctx, args, tctx)
	default:
		return "", &tools.ToolReject{
			Code: "SOURCE_HISTORY_MODE_INVALID",
			Data: map[string]any{"mode": mode, "detail": "mode must be effects, lines, mine, version, or diff"},
		}
	}
}

// historyLedger asserts the ledger read surface. It runs after path
// resolution, so scope and root rejects keep their own codes.
func historyLedger(tctx tools.ToolContext) (sourceHistoryLedger, error) {
	ledger, ok := tctx.SourceLedger.(sourceHistoryLedger)
	if !ok {
		return nil, &tools.ToolReject{
			Code: "SOURCE_HISTORY_UNAVAILABLE",
			Data: map[string]any{"detail": "the source ledger is not configured for this session"},
		}
	}
	return ledger, nil
}

// resolveHistoryPath resolves the path argument within read scope and returns
// the ledger coordinates the session's mutations record under.
func (t *SourceHistoryTool) resolveHistoryPath(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (display string, branch sourcebranch.ID, rootID, rel string, err error) {
	path, _ := args["path"].(string)
	if strings.TrimSpace(path) == "" {
		return "", "", "", "", toolkit.MissingArg("path")
	}
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", "", "", "", err
	}
	branch, rootID, rel, ok := sourceview.LedgerLocation(tctx, resolved.Abs)
	if !ok {
		return "", "", "", "", fmt.Errorf("path resolves outside attached project roots")
	}
	return resolved.DisplayPath, branch, rootID, rel, nil
}

func (t *SourceHistoryTool) runEffects(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (string, error) {
	display, branch, rootID, rel, err := t.resolveHistoryPath(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}
	resp := sourceHistoryResponse{Mode: "effects", Path: display}
	head, err := ledger.ResolveHead(ctx, tctx.ProjectID, branch, rootID, rel)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		resp.Note = sourceHistoryNoRecordNote
		return marshalSourceHistory(display, resp)
	}
	if err != nil {
		return "", fmt.Errorf("source history head: %w", err)
	}
	resp.Recorded = true
	resp.Tip = &sourceHistoryTip{State: head.State, SHA256Short: sourceview.ShortSHA(head.SHA256), VersionID: head.VersionID}
	limit := toolkit.BoundedIntArg(args, "limit", sourceHistoryDefaultLimit, 1, sourceHistoryMaxLimit).Effective
	beforeOrdinal := int64(toolkit.BoundedIntArg(args, "before_ordinal", 0, 0, 1<<62).Effective)
	page, err := ledger.QueryFileEffects(ctx, tctx.ProjectID, head.FileID, 0, beforeOrdinal, limit)
	if err != nil {
		return "", fmt.Errorf("source history effects: %w", err)
	}
	for _, effect := range page.Effects {
		if effect.BranchID != branch {
			continue
		}
		row := sourceHistoryEffect{
			Actor:     string(effect.ActorClassFor(tctx.SessionID)),
			Detail:    effect.ActorDisplay(tctx.SessionID),
			Op:        string(effect.Op),
			At:        effect.TS.UTC().Format(sourceview.StampTimeLayout),
			Tool:      effect.ToolName,
			Cause:     effect.Cause,
			VersionID: effect.AfterVersionID,
			Ordinal:   effect.Ordinal,
			FromPath:  effect.FromPath,

			gitTransitionID: effect.GitTransitionID,
		}
		row.Turn = effect.AuthoredTurn()
		resp.Effects = append(resp.Effects, row)
	}
	attachSourceHistoryGit(ctx, ledger, resp.Effects)
	resp.NextBeforeOrdinal = page.NextBeforeOrdinal
	if len(resp.Effects) == 0 && resp.NextBeforeOrdinal == 0 {
		resp.Note = "the file is tracked but no effect is recorded in this window"
	}
	return marshalSourceHistory(display, resp)
}

func (t *SourceHistoryTool) runLines(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (string, error) {
	display, branch, rootID, rel, err := t.resolveHistoryPath(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}
	res, err := ledger.QueryAttribution(ctx, tctx.ProjectID, branch, rootID, rel)
	if err != nil {
		return "", fmt.Errorf("source history lines: %w", err)
	}
	resp := sourceHistoryResponse{Mode: "lines", Path: display}
	if res.FileID == "" {
		resp.Note = sourceHistoryNoRecordNote
		return marshalSourceHistory(display, resp)
	}
	resp.Recorded = true
	resp.HeadSHA256Short = sourceview.ShortSHA(res.HeadSHA256)
	startLine := toolkit.BoundedIntArg(args, "start_line", 0, 1, 1<<30).Effective
	endLine := toolkit.BoundedIntArg(args, "end_line", 0, 1, 1<<30).Effective
	for _, iv := range res.Intervals {
		if startLine > 0 && iv.EndLine < startLine {
			continue
		}
		if endLine > 0 && iv.StartLine > endLine {
			continue
		}
		row := sourceHistoryInterval{
			StartLine: iv.StartLine, EndLine: iv.EndLine,
			Actor: string(sourceledger.ClassifyActor(iv.Origin, iv.SessionID, tctx.SessionID)),
			At:    iv.TS.UTC().Format(sourceview.StampTimeLayout),
		}
		if iv.Origin == api.SourceChangeOriginAgent {
			row.Turn = iv.Turn
			if row.Actor == string(sourceledger.ActorAgent) {
				row.Detail = sourceledger.ActorDetail(iv.ActorLabel, iv.JobID)
			}
		}
		resp.Intervals = append(resp.Intervals, row)
	}
	resp.Note = "attribution covers the last recorded state; lines outside intervals have no recorded author (pre-tracking content, or attribution reset by an external change)"
	return marshalSourceHistory(display, resp)
}

func (t *SourceHistoryTool) runMine(
	ctx context.Context,
	tctx tools.ToolContext,
) (string, error) {
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}
	resp := sourceHistoryResponse{Mode: "mine", Recorded: true}
	seen := make(map[string]struct{})
	for _, root := range tctx.Roots {
		authored, err := ledger.SessionAuthoredPaths(ctx, tctx.ProjectID, tctx.SessionID, root.ID)
		if err != nil {
			return "", fmt.Errorf("source history mine: %w", err)
		}
		for _, rel := range authored {
			display := rel
			if !root.IsPrimary && root.Label != "" {
				display = "@" + root.Label + "/" + rel
			}
			if _, dup := seen[display]; dup {
				continue
			}
			seen[display] = struct{}{}
			resp.Paths = append(resp.Paths, display)
		}
	}
	resp.Count = len(resp.Paths)
	resp.Note = "paths this session created, edited, moved, or deleted, from the ledger; foreign and user changes are not included"
	return marshalSourceHistory("", resp)
}

// attachSourceHistoryGit resolves the recorded ref movements behind the page's
// effects. A failed lookup leaves rows without git facts rather than blocking
// the history answer.
func attachSourceHistoryGit(ctx context.Context, ledger sourceHistoryLedger, effects []sourceHistoryEffect) {
	ids := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, row := range effects {
		if row.gitTransitionID == "" {
			continue
		}
		if _, ok := seen[row.gitTransitionID]; ok {
			continue
		}
		seen[row.gitTransitionID] = struct{}{}
		ids = append(ids, row.gitTransitionID)
	}
	if len(ids) == 0 {
		return
	}
	transitions, err := ledger.GitTransitionsByIDs(ctx, ids)
	if err != nil {
		return
	}
	for i := range effects {
		transition, ok := transitions[effects[i].gitTransitionID]
		if !ok {
			continue
		}
		effects[i].Git = &sourceHistoryGit{
			Kind: transition.Kind, FromRef: transition.FromRef, ToRef: transition.ToRef,
			FromCommit: sourceview.ShortSHA(transition.FromCommit), ToCommit: sourceview.ShortSHA(transition.ToCommit),
			Detail: transition.Detail,
		}
	}
}

func (t *SourceHistoryTool) runVersion(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (string, error) {
	display, _, rootID, rel, err := t.resolveHistoryPath(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	versionID, _ := args["version_id"].(string)
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_REQUIRED",
			Data: map[string]any{
				"mode":   "version",
				"detail": "version_id is required for version mode",
			},
		}
	}
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}
	ver, err := ledger.ReadRestorableVersion(ctx, tctx.ProjectID, versionID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_NOT_FOUND",
			Data: map[string]any{
				"version_id": versionID,
				"detail":     fmt.Sprintf("version %s was not found in this project", versionID),
			},
		}
	}
	if errors.Is(err, sourceledger.ErrVersionUnavailable) {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"version_id": versionID,
				"detail":     fmt.Sprintf("exact content for version %s is unavailable", versionID),
			},
		}
	}
	if err != nil {
		return "", fmt.Errorf("read source version: %w", err)
	}

	if ver.Path != rel || (ver.RootID != "" && rootID != "" && ver.RootID != rootID) {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_PATH_MISMATCH",
			Data: map[string]any{
				"version_id":    versionID,
				"path":          display,
				"expected_path": ver.Path,
				"detail":        fmt.Sprintf("version %s belongs to %s, not %s", versionID, ver.Path, display),
			},
		}
	}

	resp := sourceHistoryResponse{
		Mode:      "version",
		Path:      display,
		Recorded:  true,
		VersionID: ver.ID,
		State:     ver.State,
	}
	if ver.SHA256 != "" {
		resp.HeadSHA256Short = sourceview.ShortSHA(ver.SHA256)
	}

	if ver.State == "absent" {
		resp.Note = "the file did not exist at this version"
		return marshalSourceHistory(display, resp)
	}
	if ver.State == "directory" {
		resp.Note = "the path was a directory at this version"
		return marshalSourceHistory(display, resp)
	}

	text, ok := ver.Text()
	if !ok {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"version_id": versionID,
				"detail":     "content is binary or not valid text",
			},
		}
	}

	offset := toolkit.BoundedIntArg(args, "offset", 1, 1, 1<<30).Effective
	limit := toolkit.BoundedIntArg(args, "limit", 200, 1, 2000).Effective

	page, totalLines, endLine, hasMore := PaginateLines(text, offset, limit)
	resp.Content = hostmarker.FormatNumberedLines(page, offset)
	resp.TotalLines = totalLines
	resp.Offset = offset
	resp.Limit = limit
	resp.EndLine = endLine
	resp.Truncated = hasMore
	if hasMore {
		next := endLine + 1
		resp.NextOffset = &next
	}
	return marshalSourceHistory(display, resp)
}

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
	if versionID == "" {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_REQUIRED",
			Data: map[string]any{
				"mode":   "diff",
				"detail": "version_id is required for diff mode",
			},
		}
	}
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}

	baseVersionID, _ := args["base_version_id"].(string)
	baseVersionID = strings.TrimSpace(baseVersionID)

	var comp sourceledger.Comparison
	if baseVersionID == "current" || baseVersionID == "head" {
		head, err := ledger.ResolveHead(ctx, tctx.ProjectID, branch, rootID, rel)
		if err != nil {
			return "", &tools.ToolReject{
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
		comp, err = ledger.CompareVersionPair(ctx, tctx.ProjectID, baseVersionID, versionID)
	} else {
		comp, err = ledger.CompareVersions(ctx, tctx.ProjectID, versionID)
	}

	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_NOT_FOUND",
			Data: map[string]any{
				"version_id": versionID,
				"detail":     "one of the requested versions was not found in this project",
			},
		}
	}
	if err != nil {
		return "", fmt.Errorf("compare source versions: %w", err)
	}

	if (comp.After.Path != "" && comp.After.Path != rel) || (comp.Before.Path != "" && comp.Before.Path != rel) {
		expectedPath := comp.After.Path
		if expectedPath == "" {
			expectedPath = comp.Before.Path
		}
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_PATH_MISMATCH",
			Data: map[string]any{
				"version_id":    versionID,
				"path":          display,
				"expected_path": expectedPath,
				"detail":        fmt.Sprintf("version belongs to %s, not %s", expectedPath, display),
			},
		}
	}

	if comp.After.Availability == sourceledger.ContentUnavailable {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"version_id": versionID,
				"detail":     fmt.Sprintf("exact content for version %s is unavailable", versionID),
			},
		}
	}
	if comp.Before.Availability == sourceledger.ContentUnavailable {
		return "", &tools.ToolReject{
			Code: "SOURCE_VERSION_UNAVAILABLE",
			Data: map[string]any{
				"version_id": comp.Before.VersionID,
				"detail":     fmt.Sprintf("exact content for base version %s is unavailable", comp.Before.VersionID),
			},
		}
	}

	resp := sourceHistoryResponse{
		Mode:          "diff",
		Path:          display,
		Recorded:      true,
		VersionID:     versionID,
		BaseVersionID: comp.Before.VersionID,
	}

	oldText := comp.Before.Content
	newText := comp.After.Content
	fromLabel := fmt.Sprintf("%s (absent)", display)
	if comp.Before.VersionID != "" {
		fromLabel = fmt.Sprintf("%s (%s)", display, comp.Before.VersionID)
	}
	toLabel := fmt.Sprintf("%s (%s)", display, versionID)

	resp.Diff = sourceview.UnifiedDiff(fromLabel, toLabel, sourceview.SplitLines(oldText), sourceview.SplitLines(newText), 3)
	return marshalSourceHistory(display, resp)
}

func marshalSourceHistory(path string, resp sourceHistoryResponse) (string, error) {
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("source history encode: %w", err)
	}
	offset := 1
	if resp.Offset > 0 {
		offset = resp.Offset
	}
	return toolkit.AttachReceipt("source_history", path, offset, len(raw), resp.Truncated, string(raw)), nil
}
