package command

import (
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// writeHostRefusal adds the host's refusal to the stopped statement's stderr
// in shell voice beside the structured refusal.
func writeHostRefusal(streams commandprogram.Streams, command, form, code string) {
	writeShellRefusal(streams.Error, command, form, code, nil, nil)
}

func writeShellRefusal(w io.Writer, command, subject, code string, policyCopy map[string]string, facts map[string]any) {
	if w == nil || code == "" {
		return
	}
	if subject == "" || strings.HasPrefix(subject, "-") {
		subject = "request"
	}
	reason, _ := facts["parser_message"].(string)
	if reason == "" {
		reason = strings.TrimSpace(policyCopy["cause"])
	}
	if reason == "" {
		reason = strings.TrimSpace(policyCopy["what"])
	}
	if reason == "" {
		reason = catalogShellReason(code, command, facts)
	}
	if reason == "" {
		return
	}
	reason = strings.Join(strings.Fields(reason), " ")
	_, _ = fmt.Fprintf(w, "%s: %s: %s [%s]\n", command, subject, strings.TrimSuffix(reason, "."), code)
}

func catalogShellReason(code, command string, facts map[string]any) string {
	catalog, err := guidance.LoadHintConfigStock()
	if err != nil || catalog == nil {
		return ""
	}
	entry, ok := catalog.HintCodes[code]
	if !ok {
		return ""
	}
	data := make(map[string]any, len(facts)+2)
	for key, value := range facts {
		data[key] = value
	}
	data["command"] = command
	data["rejection_code"] = code
	what, cause, _ := guidance.RenderHintFields(code, entry, data)
	if cause = strings.TrimSpace(cause); cause != "" {
		return cause
	}
	return strings.TrimSpace(what)
}

func writeRejectRefusal(streams commandprogram.Streams, command string, cause error) {
	reject := toolrejection.AsToolReject(cause)
	if streams.Error == nil || reject == nil {
		return
	}
	subject := ""
	for _, key := range []string{"word", "path", "destination", "url", "host", "operand", "job", "signal", "pattern"} {
		if val, _ := reject.Data[key].(string); val != "" {
			subject = val
			break
		}
	}
	var policyCopy map[string]string
	if refusal, ok := guidance.RefusalFromError(cause); ok {
		policyCopy = refusal.Copy
	}
	writeShellRefusal(streams.Error, command, subject, reject.Code, policyCopy, reject.Data)
}
