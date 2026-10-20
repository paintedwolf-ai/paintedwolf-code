package detect

import (
	"context"
	"github.com/zricethezav/gitleaks/v8/detect/budget"
	"github.com/zricethezav/gitleaks/v8/report"
)

// DetectBounded preserves DetectContext policy but refuses incomplete resource-limited
// results. Limits must all be positive. No partial findings are returned on error.
// Static catalog storage and regexp engine scratch are outside the allocation meter.
func (d *Detector) DetectBounded(ctx context.Context, fragment Fragment, limits budget.Limits) (findings []report.Finding, err error) {
	defer func() {
		if err != nil {
			findings = nil
		}
	}()
	defer budget.Recover(&err)
	if ctx == nil {
		ctx = context.Background()
	}
	m := budget.New(ctx, limits)
	if limits.MaxInputBytes <= 0 || limits.MaxAllocationBytes <= 0 || limits.MaxWork <= 0 || limits.MaxMatches <= 0 || limits.MaxMatches >= int(^uint(0)>>1) {
		m.Refuse("limits", 1, 0)
	}
	if int64(len(fragment.Raw)) > limits.MaxInputBytes {
		m.Refuse("input_bytes", limits.MaxInputBytes, int64(len(fragment.Raw)))
	}
	if int64(len(fragment.Bytes)) > limits.MaxInputBytes {
		m.Refuse("input_bytes", limits.MaxInputBytes, int64(len(fragment.Bytes)))
	}
	findings = d.detectContext(ctx, fragment, m)
	return findings, nil
}
