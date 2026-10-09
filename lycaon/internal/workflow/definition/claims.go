package definition

import (
	"fmt"
	"sort"
	"strings"
)

// VerdictDecisionKey is the reserved verdict member carrying the ordered
// decision enum.
const VerdictDecisionKey = "verdict"

// VerdictClaimsType marks a verdict_schema field carrying typed claims.
const VerdictClaimsType = "claims"

// VerdictSetAsidesType marks a verdict_schema field carrying scanner groups the
// review accounts for without assessing them one by one, each with its reason.
const VerdictSetAsidesType = "set_asides"

// ClaimClass is where a claim stands. A phase's own status word is vocabulary;
// the class the phase declared for that word is what the host reads.
type ClaimClass string

const (
	// ClaimHeld claims survived their review.
	ClaimHeld ClaimClass = "held"
	// ClaimFailed claims the review overturned.
	ClaimFailed ClaimClass = "failed"
	// ClaimOpen claims no review has settled.
	ClaimOpen ClaimClass = "open"
)

func parseClaimStatuses(phaseID string, raw map[string]string) (map[string]ClaimClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]ClaimClass, len(raw))
	for word, class := range raw {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			return nil, fmt.Errorf("phase %q: review_loop.claim_statuses has an empty status", phaseID)
		}
		switch c := ClaimClass(strings.TrimSpace(class)); c {
		case ClaimHeld, ClaimFailed, ClaimOpen:
			out[word] = c
		default:
			return nil, fmt.Errorf("phase %q: review_loop.claim_statuses[%s] is %q (want held, failed, or open)", phaseID, word, class)
		}
	}
	return out, nil
}

func claimStatusesYAML(in map[string]ClaimClass) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for word, class := range in {
		out[word] = string(class)
	}
	return out
}

func cloneClaimStatuses(in map[string]ClaimClass) map[string]ClaimClass {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ClaimClass, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// CarriesClaims reports whether the phase's verdict schema has a claims member.
func (d ReviewLoopDef) CarriesClaims() bool {
	for field, kind := range d.VerdictSchema {
		if field != VerdictDecisionKey && strings.TrimSpace(kind) == VerdictClaimsType {
			return true
		}
	}
	return false
}

// Decisions lists the declared verdict values with the terminal value first.
func (d ReviewLoopDef) Decisions() []string {
	var out []string
	for _, v := range strings.Split(d.VerdictSchema[VerdictDecisionKey], "|") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// StatusWords lists the declared status words in stable order.
func (d ReviewLoopDef) StatusWords() []string {
	out := make([]string, 0, len(d.ClaimStatuses))
	for word := range d.ClaimStatuses {
		out = append(out, word)
	}
	sort.Strings(out)
	return out
}

// ClassOf is the class a status word settles a claim into. A word the phase
// never declared settles nothing.
func (d ReviewLoopDef) ClassOf(status string) ClaimClass {
	if class, ok := d.ClaimStatuses[strings.ToLower(strings.TrimSpace(status))]; ok {
		return class
	}
	return ClaimOpen
}

// VerdictCoverageType declares an evidence-backed assessment of review obligations.
const VerdictCoverageType = "coverage_review"

// CarriesCoverage reports whether this phase assesses coverage.
func (d ReviewLoopDef) CarriesCoverage() bool {
	for _, kind := range d.VerdictSchema {
		if kind == VerdictCoverageType {
			return true
		}
	}
	return false
}
