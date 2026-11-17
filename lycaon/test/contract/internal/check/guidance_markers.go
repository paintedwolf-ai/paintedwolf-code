package check

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
)

// HostRejectCode returns the Code: line the host wrote into a reject or kick block.
func HostRejectCode(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, hostmarker.CodeLine); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// InjectAnchor returns the inform anchor an inject: emit target names.
func InjectAnchor(emit string) (string, bool) {
	target, ok := strings.CutPrefix(strings.TrimSpace(emit), guidance.EmitInjectPrefix)
	if !ok || strings.TrimSpace(target) == "" {
		return "", false
	}
	return "inject." + strings.TrimSpace(target), true
}
