package guidance

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/noticeerr"
	wire "github.com/lycaon/lycaon/pkg/api"
	"strings"
)

const (
	// MaxOffenderDisplay caps tokens shown in hint template sample vars.
	MaxOffenderDisplay = 8
	// MaxOffenderSampleChars caps total sample string length for hint templates.
	MaxOffenderSampleChars = 240
)

// FormatOffenderReport builds bounded reject display fields from full offender tokens.
func FormatOffenderReport(tokens []string) evidence.OffenderReport {
	count := len(tokens)
	if count == 0 {
		return evidence.OffenderReport{}
	}
	display := make([]string, 0, MaxOffenderDisplay)
	remaining := MaxOffenderSampleChars
	for _, tok := range tokens {
		if len(display) >= MaxOffenderDisplay {
			break
		}
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		extra := len(tok)
		if len(display) > 0 {
			extra++ // comma separator
		}
		if len(display) > 0 && remaining-extra < 0 {
			break
		}
		if len(display) == 0 && extra > MaxOffenderSampleChars {
			if len(tok) > MaxOffenderSampleChars {
				// The cap is a byte offset, so it can land mid-rune.
				tok = strings.ToValidUTF8(tok[:MaxOffenderSampleChars], "")
			}
		}
		display = append(display, tok)
		remaining -= extra
	}
	omitted := count - len(display)
	if omitted < 0 {
		omitted = 0
	}
	return evidence.OffenderReport{
		Count:   count,
		Sample:  strings.Join(display, ", "),
		Omitted: omitted,
	}
}

func addOffender(out *[]string, seen map[string]struct{}, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	if _, ok := seen[token]; ok {
		return
	}
	seen[token] = struct{}{}
	*out = append(*out, token)
}

// ErrGroundingEscalated is returned when circuit breaker blocks coordinator prompts.
var ErrGroundingEscalated error = noticeerr.NewSentinel("grounding_escalated", wire.NoticeCodeGroundingEscalated)

// GroundingFriction is what remains of a session tree's grounding-reject
// budget after a reject: the tighter of its prompt and cycle ceilings.
type GroundingFriction struct {
	Remaining int
}

// Exhausted reports that the budget admits no further retry.
func (f GroundingFriction) Exhausted() bool {
	return f.Remaining <= 0
}

// Classification-free grounding contract.
//
// Grounding never asks "does this token look like a path/URL?" as a gate.
// Verbatim-substring verification (opaque/command shapes) and observed-set
// membership (file_region/url) are the only production verdict paths.
const (
	// GroundingVerdictPrimary documents verbatim-first grounding for command/opaque shapes.
	GroundingVerdictPrimary = "verbatim_substring"

	// LeakDetectionMode documents observed-set membership for prose leak detection.
	LeakDetectionMode = "observed_set_membership"

	// LeakSeverityReject is a structured-observed citation in prose outside the typed channel.
	LeakSeverityReject = "reject"

	// LeakSeverityAdvisory is a lower-trust observed citation in prose — surfaced, not partial.
	LeakSeverityAdvisory = "advisory"
)
