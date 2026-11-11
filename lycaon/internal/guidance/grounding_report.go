package guidance

import (
	"cmp"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/noticeerr"
	wire "github.com/lycaon/lycaon/pkg/api"
	"slices"
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

// groundingHintHandles orders ledger handles for rejection samples so recent
// observations across kinds and offender-matching tools surface before older handles.
func groundingHintHandles(offenders []string, ev evidence.Ledger) []string {
	handles := evidence.HandlesSorted(ev)
	if len(handles) <= 1 {
		return handles
	}
	offenderKinds := offenderHandleKinds(offenders)
	byGroup := make(map[string][]string)
	ordinals := make(map[string]int, len(handles))
	kinds := make(map[string]string, len(handles))
	for _, h := range handles {
		kind, ord := evidence.ParseHandleOrdinal(h)
		kinds[h] = kind
		ordinals[h] = ord
		group := handleScopeGroup(h, kind)
		byGroup[group] = append(byGroup[group], h)
	}
	rankInGroup := make(map[string]int, len(handles))
	for _, groupHandles := range byGroup {
		slices.SortFunc(groupHandles, func(a, b string) int {
			return cmp.Compare(ordinals[b], ordinals[a])
		})
		for rank, h := range groupHandles {
			rankInGroup[h] = rank
		}
	}
	priorities := make(map[string]handlePriority, len(handles))
	for _, h := range handles {
		rec := ev.Handles[h]
		matched := len(offenderKinds) > 0 && (offenderKinds[kinds[h]] || offenderKinds[rec.Kind] || offenderKinds[rec.SourceTool])
		priorities[h] = handlePriority{
			gate:         boolRank(rec.IsGate()),
			offenderRank: boolRank(!matched),
			superseded:   boolRank(rec.SupersededBy != ""),
			rankInGroup:  rankInGroup[h],
			ordinal:      ordinals[h],
			handle:       h,
		}
	}
	out := append([]string(nil), handles...)
	slices.SortFunc(out, func(a, b string) int {
		return compareHandlePriority(priorities[a], priorities[b])
	})
	return out
}

type handlePriority struct {
	gate         int
	offenderRank int
	superseded   int
	rankInGroup  int
	ordinal      int
	handle       string
}

func compareHandlePriority(a, b handlePriority) int {
	if c := cmp.Compare(a.gate, b.gate); c != 0 {
		return c
	}
	if c := cmp.Compare(a.offenderRank, b.offenderRank); c != 0 {
		return c
	}
	if c := cmp.Compare(a.superseded, b.superseded); c != 0 {
		return c
	}
	if c := cmp.Compare(a.rankInGroup, b.rankInGroup); c != 0 {
		return c
	}
	if c := cmp.Compare(b.ordinal, a.ordinal); c != 0 {
		return c
	}
	return cmp.Compare(a.handle, b.handle)
}

func boolRank(v bool) int {
	if v {
		return 1
	}
	return 0
}

func offenderHandleKinds(offenders []string) map[string]bool {
	var out map[string]bool
	for _, raw := range offenders {
		token := strings.TrimSpace(raw)
		if i := strings.IndexAny(token, " \t("); i >= 0 {
			token = token[:i]
		}
		if p, _, ok := evidence.SplitPathLineToken(token); ok && strings.Contains(p, "#") {
			token = p
		}
		kind, ord := evidence.ParseHandleOrdinal(token)
		if ord <= 0 || kind == "" {
			continue
		}
		if out == nil {
			out = make(map[string]bool)
		}
		out[kind] = true
	}
	return out
}

func handleScopeGroup(handle, kind string) string {
	if idx := strings.LastIndex(handle, ":"); idx >= 0 {
		return handle[:idx+1] + kind
	}
	return kind
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
