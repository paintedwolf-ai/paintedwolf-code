package extpacks

import (
	"fmt"
	"sort"
	"strings"
)

// describeDiagnostics renders load failures as the unit and the reason.
func describeDiagnostics(diags []Diagnostic) string {
	lines := make([]string, 0, len(diags))
	for _, d := range diags {
		switch {
		case d.UnitID != "":
			lines = append(lines, fmt.Sprintf("%s: %s", d.UnitID, d.Message))
		case d.PackID != "":
			lines = append(lines, fmt.Sprintf("pack %s: %s", d.PackID, d.Message))
		default:
			lines = append(lines, d.Message)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "; ")
}
