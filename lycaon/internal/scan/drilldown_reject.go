package scan

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/guidance"
)

// Drill-down reject codes (scan_list / scan_summary / scan_query).
const (
	DrilldownRejectNotFound     = "SCAN_NOT_FOUND"
	DrilldownRejectNotComplete  = "SCAN_NOT_COMPLETE"
	DrilldownRejectPassNotFound = "SCAN_PASS_NOT_FOUND"
)

// DrilldownReject is a structured scan drill-down validation failure.
type DrilldownReject struct {
	Code string
	Data map[string]any
}

func (e *DrilldownReject) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// FormatDrilldownReject maps DrilldownReject to a structured reject block for the model.
func FormatDrilldownReject(err error, formatter *guidance.StaticRejectFormatter) error {
	if err == nil {
		return nil
	}
	var reject *DrilldownReject
	if !errors.As(err, &reject) || reject == nil || reject.Code == "" {
		return err
	}
	return toolrejection.FormatDecisionReject(reject.Code, reject.Data, formatter)
}

func MapDrilldownReject(err error, rejectFmt *guidance.StaticRejectFormatter) error {
	var reject *DrilldownReject
	if errors.As(err, &reject) {
		return FormatDrilldownReject(reject, rejectFmt)
	}
	return err
}
