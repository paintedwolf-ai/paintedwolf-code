package guidance

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// Markers the host writes into blocks it composed; matching one reads back
// machine state. Markers that internal/evidence or Den also read live in
// internal/hostmarker.
const (
	markerToolFeedback = hostmarker.GuidanceBlockOpen + "Tool feedback"
	markerSpecPosture  = hostmarker.GuidanceBlockOpen + "Spec posture"
	markerRequiredNext = hostmarker.GuidanceBlockOpen + "REQUIRED NEXT"
	markerRejected     = hostmarker.Rejected
	codeLinePrefix     = hostmarker.CodeLine
)

// MarkerSecretReceipt heads the line a seam writes after a redacted send.
// The enricher reads it back and replaces it with the registered hint copy,
// so the agent-facing teaching has one spelling in the registry.
const MarkerSecretReceipt = "[lycaon secret-receipt]"

// FormatSecretReceiptMarker composes the seam-side marker line.
func FormatSecretReceiptMarker(token string, count int) string {
	return "\n" + MarkerSecretReceipt + " token=" + token + " count=" + strconv.Itoa(count)
}

// ParseSecretReceiptMarker reads a marker line back.
func ParseSecretReceiptMarker(line string) (token string, count int, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), MarkerSecretReceipt)
	if !found {
		return "", 0, false
	}
	for _, field := range strings.Fields(rest) {
		if v, isToken := strings.CutPrefix(field, "token="); isToken {
			token = v
		}
		if v, isCount := strings.CutPrefix(field, "count="); isCount {
			count, _ = strconv.Atoi(v)
		}
	}
	return token, count, token != ""
}

// MarkerPeriodHint heads the line web_search writes when a query carried a
// bare past-year token and no window was declared. The enricher replaces it
// with the registered hint, so the teaching has one spelling in the registry.
const MarkerPeriodHint = "[lycaon period-hint]"

// FormatPeriodHintMarker composes the seam-side marker line.
func FormatPeriodHintMarker(year string) string {
	return "\n" + MarkerPeriodHint + " year=" + year
}

// ParsePeriodHintMarker reads a marker line back.
func ParsePeriodHintMarker(line string) (year string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), MarkerPeriodHint)
	if !found {
		return "", false
	}
	for _, field := range strings.Fields(rest) {
		if v, isYear := strings.CutPrefix(field, "year="); isYear {
			year = v
		}
	}
	return year, year != ""
}

// Worker charter markers — the rows carrying a leg's scope and assignment.
// Prompt assembly reads them back to pin those rows through the context fit.
const (
	MarkerWorkerTaskPreamble   = "<!-- lycaon-worker-task-preamble:v1 -->"
	MarkerWorkerTaskAssignment = "<!-- lycaon-worker-task-assignment:v1 -->"
)

// ContextTrimNotice is the row the deterministic fit emits when it drops history.
// It is constant text because the fit runs on the latency path and a later pass
// recognizes an earlier pass's notice by exact match.
const ContextTrimNotice = `[host:context-trimmed]

Earlier rows of this session — tool results and the calls that produced them — no longer fit the model window and have left your context. Anything pinned above is intact; the evidence behind it is not.

Do not re-run searches to rebuild what was trimmed. The same call returns the same bytes, and the window that dropped them will drop them again. Work from what is in front of you: if a fact you need is genuinely gone, state it as unresolved in your report rather than searching for it a second time.`

// CarriesWorkerCharter reports whether content carries a leg's scope or assignment.
func CarriesWorkerCharter(content string) bool {
	return strings.Contains(content, MarkerWorkerTaskPreamble) ||
		strings.Contains(content, MarkerWorkerTaskAssignment)
}

// HostRejectCode returns the Code: line the host wrote into a reject or kick block.
func HostRejectCode(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, codeLinePrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// Code family prefixes name the registry a code came from.
const (
	codeFamilySpec        = "SPEC_"
	codeFamilyCoordinator = "COORDINATOR_"
)

func codeFamilyIs(code string, families ...string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	for _, family := range families {
		if strings.HasPrefix(code, family) {
			return true
		}
	}
	return false
}
