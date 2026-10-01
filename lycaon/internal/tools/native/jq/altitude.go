package jq

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

func specificsAffordance(ctx context.Context) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyJQSpecificsRef, nil)
	if err != nil {
		return ""
	}
	return out
}

func zoomHint(ctx context.Context, count, bytes int) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyJQZoomRef, map[string]any{
		"count": count,
		"bytes": bytes,
	})
	if err != nil {
		return ""
	}
	return out
}

func buildZoomedOutResponse(ctx context.Context, resp response, results []any, totalBytes int) response {
	out := resp
	out.Values = nil
	out.Shape = streamShape(results)
	out.Total = len(results)
	out.Note = toolkit.AppendNote(out.Note, specificsAffordance(ctx))
	out.Diagnostics = &diagnostics{Hint: zoomHint(ctx, len(results), totalBytes)}
	out.Selected = 0
	return out
}
