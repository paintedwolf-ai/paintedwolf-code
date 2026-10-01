package survey

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

func buildGrepZoomedOutResponse(ctx context.Context, resp grepResponse, collected []grepMatch) grepResponse {
	out := resp
	out.View = surveyViewDigest
	out.Matches = nil
	out.Total = len(collected)
	out.Note = toolkit.AppendNote(out.Note, grepSpecificsAffordance(ctx))
	out.Selected = 0
	return out
}
