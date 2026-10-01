package extpacks

import (
	"fmt"
	"strings"
)

// UnsupportedFormatError rejects an unknown document format.
type UnsupportedFormatError struct {
	Doc   string // extension.yaml | extensions.yaml | extensions.lock.yaml
	Field string // manifest_version | format | lock_format
	Got   int
	Want  int
}

func (e *UnsupportedFormatError) Error() string {
	return fmt.Sprintf("unsupported %s %s %d (want %d)", e.Doc, e.Field, e.Got, e.Want)
}

// joinUnitDiagnostics renders diagnostics as "<unit>: <why>" so a rejection
// names what to open.
func joinUnitDiagnostics(diags []Diagnostic) string {
	parts := make([]string, 0, len(diags))
	for _, d := range diags {
		if d.UnitID == "" {
			parts = append(parts, d.Message)
			continue
		}
		parts = append(parts, d.UnitID+": "+d.Message)
	}
	return strings.Join(parts, "; ")
}
