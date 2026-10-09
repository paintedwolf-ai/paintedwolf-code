package survey

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

const (
	hostDiffMaxPathBytes    = 2 << 20   // 2 MiB per file
	hostDiffMaxOutputBytes  = 256 << 10 // 256 KiB unified diff output
	hostDiffDefaultContext  = 3
	hostDiffMaxContextLines = 10
)

// DiffTool returns a unified diff between two repo-relative file paths.
type DiffTool struct {
	Boundary *sandbox.Boundary
}

type diffResponse struct {
	PathA            string `json:"path_a"`
	PathB            string `json:"path_b"`
	Diff             string `json:"diff"`
	ContextLines     int    `json:"context_lines"`
	Truncated        bool   `json:"truncated"`
	TruncationBanner string `json:"truncation_banner,omitempty"`
}

func (t *DiffTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pathA, pathB, err := parseDiffPaths(args)
	if err != nil {
		return "", err
	}
	contextArg := toolkit.BoundedIntArg(args, "context_lines", hostDiffDefaultContext, 0, hostDiffMaxContextLines)
	prof := tctx.ProfileID()

	textA, relA, err := t.loadDiffFile(ctx, tctx, pathA, prof)
	if err != nil {
		return "", err
	}
	textB, relB, err := t.loadDiffFile(ctx, tctx, pathB, prof)
	if err != nil {
		return "", err
	}

	diffText := sourceview.UnifiedDiff(relA, relB, sourceview.SplitLines(textA), sourceview.SplitLines(textB), contextArg.Effective)
	truncated := false
	var bannerParts []string
	if len(diffText) > hostDiffMaxOutputBytes {
		diffText, truncated = sourceview.TruncateDiffOutput(diffText, hostDiffMaxOutputBytes)
		bannerParts = append(bannerParts, fmt.Sprintf("diff output capped at %d bytes", hostDiffMaxOutputBytes))
	}
	bannerParts = toolkit.AppendClampBanner(bannerParts, "context_lines", contextArg)

	resp := diffResponse{
		PathA:        relA,
		PathB:        relB,
		Diff:         diffText,
		ContextLines: contextArg.Effective,
		Truncated:    truncated,
	}
	if len(bannerParts) > 0 {
		resp.TruncationBanner = toolkit.TruncationBanner(strings.Join(bannerParts, "; "))
	}

	raw, err := toolkit.MarshalResponse(resp, resp.TruncationBanner)
	if err != nil {
		return "", err
	}
	receiptPath := relA + " ↔ " + relB
	return toolkit.AttachReceipt("diff", receiptPath, 2, len(raw), truncated || resp.TruncationBanner != "", raw), nil
}

func parseDiffPaths(args map[string]any) (pathA, pathB string, err error) {
	pathA, _ = args["path_a"].(string)
	pathB, _ = args["path_b"].(string)
	pathA = strings.TrimSpace(pathA)
	pathB = strings.TrimSpace(pathB)
	if pathA == "" {
		return "", "", toolkit.MissingArg("path_a")
	}
	if pathB == "" {
		return "", "", toolkit.MissingArg("path_b")
	}
	return pathA, pathB, nil
}

func (t *DiffTool) loadDiffFile(ctx context.Context, tctx tools.ToolContext, relPath, profileID string) (content, relSlash string, err error) {
	if sandbox.HasParentTraversal(relPath) {
		return "", "", toolkit.PathEscapeReject(relPath)
	}
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, relPath)
	if err != nil {
		return "", "", err
	}
	fullPath := resolved.Abs
	relSlash = filepath.ToSlash(resolved.DisplayPath)
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", sourceview.PathNotFound("diff", relPath, fullPath)
		}
		return "", "", fmt.Errorf("diff %s: %w", relPath, err)
	}
	if info.IsDir() {
		return "", "", &toolrejection.ToolReject{
			Code: "READ_IS_DIRECTORY",
			Data: map[string]any{"path": relSlash},
		}
	}
	if info.Size() > hostDiffMaxPathBytes {
		return "", "", &toolrejection.ToolReject{
			Code: "DIFF_FILE_TOO_LARGE",
			Data: map[string]any{
				"path":           relSlash,
				"max_path_bytes": hostDiffMaxPathBytes,
				"size":           info.Size(),
			},
		}
	}
	st, err := sourceview.LoadText(ctx, "diff", sourceview.AccessRead, tctx, resolved)
	if err != nil {
		return "", "", fmt.Errorf("diff %s: %w", relPath, err)
	}
	return st.Content, relSlash, nil
}
