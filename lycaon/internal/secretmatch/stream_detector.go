package secretmatch

import (
	"context"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/detect/budget"
	"github.com/zricethezav/gitleaks/v8/report"
)

func (m *Matcher) boundedFindings(ctx context.Context, text string, bytes, work int64, reserve func(int64) error, release func(int64)) ([]report.Finding, error) {
	return m.detector.DetectBounded(ctx, detect.Fragment{Raw: text}, budget.Limits{
		MaxInputBytes: int64(m.screenWindowBytes()), MaxAllocationBytes: bytes, MaxWork: work,
		MaxMatches: max(1, int(bytes/128)), Reserve: reserve, Release: release,
	})
}
