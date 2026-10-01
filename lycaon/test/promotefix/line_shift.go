// Package promotefix holds shared promotion test fixtures.
package promotefix

import "strings"

// LineShiftBodies returns a line-shift conflict.
func LineShiftBodies() (base, primary, branch string) {
	base = strings.Repeat("line\n", 50)
	return base, strings.Repeat("line\n", 55), base + "trap\n"
}
