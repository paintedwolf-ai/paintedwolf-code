package secretmatch

import (
	"context"
	"errors"

	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/detect/budget"
	"github.com/zricethezav/gitleaks/v8/report"
)

func (m *Matcher) boundedFindings(ctx context.Context, text string, bytes, work int64, reserve func(int64) error, release func(int64)) ([]report.Finding, error) {
	findings, err := m.detector.DetectBounded(ctx, detect.Fragment{Raw: text}, budget.Limits{ //nolint:staticcheck // DetectBounded requires detect.Fragment to enforce bounded allocation and work.
		MaxInputBytes: int64(m.screenWindowBytes()), MaxAllocationBytes: bytes, MaxWork: work,
		MaxMatches: max(1, int(bytes/128)), Reserve: reserve, Release: release,
	})
	var limit *budget.Error
	if errors.As(err, &limit) {
		return nil, &StreamLimitError{
			Resource: streamBudgetResource(limit.Resource),
			Maximum:  limit.Maximum,
			Observed: limit.Observed,
		}
	}
	return findings, err
}

func streamBudgetResource(resource string) string {
	switch resource {
	case "work":
		return "screen_work_units"
	case "allocation_bytes":
		return "screen_memory_bytes"
	case "input_bytes":
		return "screen_input_bytes"
	case "match_candidates":
		return "screen_match_candidates"
	default:
		return resource
	}
}
