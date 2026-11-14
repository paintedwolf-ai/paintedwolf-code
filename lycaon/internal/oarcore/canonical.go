package oarcore

import (
	"strconv"
	"strings"
)

// CanonicalDouble is the finite binary64 rendering defined by [OAR-COPY-9].
func CanonicalDouble(v float64) string {
	if v == 0 {
		return "0.0"
	}
	text := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}
	return text
}
