package survey

import (
	"context"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// readHasExpressedScope reports whether the caller expressed literal scope.
func readHasExpressedScope(mode string, args map[string]any, totalLines int) bool {
	if readSymbolArg(args) != "" {
		return true
	}
	if readHasRanges(args) {
		return true
	}
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["limit"]; ok {
		return true
	}
	if mode == "content" {
		return true
	}
	return totalLines <= readcaps.AutoOutlineThreshold
}

func readSpecificsAffordance(ctx context.Context) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyReadSpecificsRef, nil)
	if err != nil {
		return ""
	}
	return out
}

func buildReadLiteralFullResponse(path, text string) ReadResponse {
	totalLines := toolkit.CountLines(text)
	page, _, endLine, _ := PaginateLines(text, 1, totalLines)
	resp := ReadResponse{
		Path:       path,
		Mode:       "content",
		Content:    formatReadContent(page, 1),
		TotalLines: totalLines,
		Offset:     1,
		Limit:      totalLines,
		EndLine:    endLine,
		Truncated:  false,
	}
	return resp
}

func buildReadZoomedOutlineResponse(ctx context.Context, path, text string, outline fileoutline.Result) ReadResponse {
	resp := buildReadOutlineResponse(path, outline)
	resp.Note = toolkit.AppendNote(resp.Note, readSpecificsAffordance(ctx))

	totalCandidates := len(outline.Symbols)
	if outline.LogDigest != nil {
		totalCandidates = 1
	}
	resp.Total = totalCandidates
	resp.Selected = 0
	return resp
}
