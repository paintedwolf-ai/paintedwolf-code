package survey

import (
	"fmt"
	"strings"
)

func formatReadContent(page []string, startLine int) string {
	if len(page) == 0 {
		return ""
	}
	var b strings.Builder
	for i, line := range page {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d: %s", startLine+i, line)
	}
	return b.String()
}
