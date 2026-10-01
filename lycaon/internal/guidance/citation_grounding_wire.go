package guidance

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// GroundingSampleCap bounds how many tokens each api.CitationGrounding sample array
// carries to the wire. It bounds display only; ObservedSampleCap bounds how many
// citations the host binds into the report.
const GroundingSampleCap = 12

// SampleGroundingStrings bounds a token list for an api.CitationGrounding sample field.
// The returned slice never aliases the input, so callers may retain it on the wire type.
func SampleGroundingStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := append([]string(nil), items...)
	if len(out) > GroundingSampleCap {
		out = out[:GroundingSampleCap]
	}
	return out
}

// FillObservedSamples attaches bounded observed path/URL samples to a grounding envelope.
// Counts report the full observed set; only the sample arrays are bounded.
func FillObservedSamples(g *api.CitationGrounding, ev evidence.Ledger) {
	if g == nil {
		return
	}
	if paths := evidence.ObservedPathsSorted(ev); len(paths) > 0 {
		g.ObservedPathCount = len(paths)
		g.ObservedPathsSample = SampleGroundingStrings(paths)
	}
	if urls := evidence.ObservedURLsSorted(ev); len(urls) > 0 {
		g.ObservedURLCount = len(urls)
		g.ObservedURLsSample = SampleGroundingStrings(urls)
	}
}

// FillProseSamples attaches bounded prose duplicate/advisory samples to a grounding
// envelope. Duplicates are citations already carried in typed fields; advisories are
// citations that appeared only in prose. Counts report the full token set.
func FillProseSamples(g *api.CitationGrounding, duplicateTokens, advisoryTokens []string) {
	if g == nil {
		return
	}
	if n := len(duplicateTokens); n > 0 {
		g.ProseLeakCount = n
		g.ProseLeaksSample = SampleGroundingStrings(duplicateTokens)
	}
	if n := len(advisoryTokens); n > 0 {
		g.ProseAdvisoryCount = n
		g.ProseAdvisoriesSample = SampleGroundingStrings(advisoryTokens)
	}
}
