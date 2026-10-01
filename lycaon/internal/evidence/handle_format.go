package evidence

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// HandleGrammar matches evidence handles with optional ledger scopes.
var HandleGrammar = regexp.MustCompile(`^` + hostmarker.EvidenceHandlePattern + `$`)

// handleTokenPrefixRE captures a leading evidence handle and its display suffix.
var handleTokenPrefixRE = regexp.MustCompile(`^(` + hostmarker.EvidenceHandlePattern + `)(.*)$`)

// FormatHandle returns kind#ordinal (ordinal is 1-based per kind).
func FormatHandle(kind string, ordinal int) string {
	if ordinal < 1 {
		ordinal = 1
	}
	return fmt.Sprintf("%s#%d", kind, ordinal)
}

// terminalHandle removes ledger scopes without parsing the terminal value.
func terminalHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	hash := strings.IndexByte(handle, '#')
	if hash < 0 {
		return handle
	}
	if separator := strings.LastIndexByte(handle[:hash], ':'); separator >= 0 {
		return handle[separator+1:]
	}
	return handle
}
