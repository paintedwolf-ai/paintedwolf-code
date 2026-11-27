package command

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// writeLimitRefusal writes a typed resource stop as one shell-voice line; the
// structured rejection keeps the exact limit and source facts.
func writeLimitRefusal(w io.Writer, command, subject string, reject *toolrejection.ToolReject) {
	if w == nil || !programResourceRefusal(reject) {
		return
	}
	if subject == "" || strings.HasPrefix(subject, "-") {
		subject = "request"
	}
	reason := "limit exceeded"
	if catalog, err := guidance.LoadHintConfigStock(); err == nil && catalog != nil {
		if entry, ok := catalog.HintCodes[reject.Code]; ok {
			if what, _, _ := guidance.RenderHintFields(context.Background(), reject.Code, entry, reject.Data, nil); what != "" {
				reason = strings.TrimSuffix(strings.Join(strings.Fields(what), " "), ".")
			}
		}
	}
	line := fmt.Sprintf("%s: %s: %s [%s]\n", command, subject, reason, reject.Code)
	if hw, ok := w.(hostOriginWriter); ok {
		_, _ = hw.WriteHost([]byte(line))
		return
	}
	_, _ = io.WriteString(w, line)
}
