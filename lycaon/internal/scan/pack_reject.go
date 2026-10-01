package scan

import (
	"errors"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
)

// PackReject is a structured scan_pack failure surfaced via guidance hint codes.
type PackReject struct {
	Code string
	Data map[string]any
}

func (e *PackReject) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// FormatPackReject maps PackReject to a structured reject block for the model.
func FormatPackReject(err error, formatter *guidance.StaticRejectFormatter) error {
	if err == nil {
		return nil
	}
	var packErr *PackReject
	if !errors.As(err, &packErr) || packErr == nil || packErr.Code == "" {
		return err
	}
	return tools.FormatDecisionReject(packErr.Code, packErr.Data, formatter)
}
