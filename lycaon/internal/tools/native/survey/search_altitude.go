package survey

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
)

const surveyViewDigest = "digest"

func grepSpecificsAffordance(ctx context.Context) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyGrepSpecificsRef, nil)
	if err != nil {
		return ""
	}
	return out
}

func findSpecificsAffordance(ctx context.Context) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyFindSpecificsRef, nil)
	if err != nil {
		return ""
	}
	return out
}

func grepIsUnbounded(args map[string]any, truncated bool) bool {
	if _, ok := args["offset"]; ok {
		return false
	}
	if _, ok := args["max_matches"]; ok {
		return false
	}
	return truncated
}

// findIsUnbounded is true only for a true wide walk: truncated host-cap results
// with no expressed paging and no expressed walk root / name_glob. A concrete
// subpath or name_glob is targeted (literal page), matching list_dir's subpath rule.
func findIsUnbounded(args map[string]any, truncated bool) bool {
	if evidence.FindArgsExpressedScope(args) {
		return false
	}
	return truncated
}
