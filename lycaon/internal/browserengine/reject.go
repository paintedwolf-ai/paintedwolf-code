package browserengine

import "fmt"

// RejectError is a typed browser reject. It lives here because the engine's own
// refusals carry it and internal/browser builds the rest on top, so both layers
// and every caller that branches on Code share one type.
type RejectError struct {
	Code string
	Data map[string]any
}

func (e *RejectError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) == 0 {
		return e.Code
	}
	return fmt.Sprintf("%s %v", e.Code, e.Data)
}

// Reject builds a typed reject.
func Reject(code string, data map[string]any) error {
	return &RejectError{Code: code, Data: data}
}
