package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// enrichAccumulator holds the result body and the codes raised against it.
type enrichAccumulator struct {
	body  string
	facts ToolResultFacts
}

func newEnrichAccumulator(body string, raised ToolResultFacts) *enrichAccumulator {
	return &enrichAccumulator{body: body, facts: raised}
}

func (a *enrichAccumulator) raised(code string) bool {
	return a.facts.HasCode(code)
}

// raise records a banner. Empty msg falls back to the code.
func (a *enrichAccumulator) raise(code, msg string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	if a.raised(code) {
		return false
	}
	if strings.TrimSpace(msg) == "" {
		msg = code
	}
	a.body = AppendOutputBanner(a.body, code, msg)
	a.facts = a.facts.WithCode(code)
	return true
}

func (a *enrichAccumulator) raiseRejection(code, msg string) bool {
	if !a.raise(code, msg) {
		return false
	}
	a.facts = a.facts.WithOutcome(api.ToolResultOutcomeRejected)
	return true
}

// appendLine adds a trailing line with no code. Duplicate lines are dropped.
func (a *enrichAccumulator) appendLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.Contains(a.body, line) {
		return
	}
	if strings.TrimSpace(a.body) == "" {
		a.body = line
		return
	}
	a.body += "\n\n" + line
}
