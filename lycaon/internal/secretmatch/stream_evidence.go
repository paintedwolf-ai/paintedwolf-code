package secretmatch

import (
	"context"
	"strings"
)

// streamEvidence snapshots source-owned immutable values and charges only the
// per-screen copied descriptors, serializer scratch, and retained needle index.
func (m *Matcher) streamEvidence(ctx context.Context, memory *streamMemory) ([]streamNeedle, error) {
	local, _ := ctx.Value(screeningValuesKey{}).([]Remembered)
	var source []HarvestedValue
	if m.harvestSource != nil {
		source = m.harvestSource(ctx)
	}
	if err := memory.take(int64(len(source)+len(local)) * 512); err != nil {
		return nil, err
	}
	values := make([]HarvestedValue, 0, len(source)+len(local))
	current := make(map[string]bool, len(local))
	for _, v := range local {
		current[v.Secret] = true
	}
	for _, v := range source {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if IsManagedRule(v.RuleID) && current[v.Secret] {
			continue
		}
		values = append(values, v)
	}
	for _, v := range local {
		if m.fingerprinter != nil {
			values = append(values, HarvestedValue{Secret: v.Secret, Name: v.Name, Container: v.Origin, RuleID: v.RuleID, Title: v.Title, Source: v.Source, Reference: v.Reference, Retired: v.Retired, NonDisclosable: v.NonDisclosable, Fingerprint: m.fingerprinter.Fingerprint(v.Secret)})
		}
	}
	var out []streamNeedle
	for _, v := range values {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(v.Secret) > MaxSecretBytes {
			return nil, &StreamLimitError{"secret_bytes", MaxSecretBytes, int64(len(v.Secret))}
		}
		// All serializer forms plus eligible line spellings are linear in source
		// bytes. Reserve their temporary backing capacities before using the existing
		// serializer; duplicates can only reduce the retained index.
		allowance := int64(len(v.Secret))*384 + 4096
		if err := memory.take(allowance); err != nil {
			return nil, err
		}
		needles := harvestNeedles(v.Secret, MinNeedleRunes(v.NonDisclosable))
		retained := int64(len(needles)) * 512
		for _, n := range needles {
			retained += int64(len(n.text))
		}
		if retained > allowance {
			return nil, &StreamLimitError{"screen_memory_bytes", memory.maximum, memory.used + retained - allowance}
		}
		if err := memory.take(int64(len(out)+len(needles)) * 512); err != nil {
			return nil, err
		}
		next := make([]streamNeedle, 0, len(out)+len(needles))
		next = append(next, out...)
		for _, n := range needles {
			next = append(next, streamNeedle{needle: n, value: v})
		}
		memory.release(int64(cap(out)) * 512)
		out = next
		memory.release(allowance - retained)
	}
	memory.release(int64(cap(values)) * 512)
	return out, nil
}

func (m *Matcher) streamHarvest(ctx context.Context, text string, base, until int64, eof bool, needles []streamNeedle, hits *streamHits) error {
	for i := range needles {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := &needles[i]
		if !n.value.NonDisclosable && n.occurrences >= maxHarvestOccurrences {
			n.cursor = until
			continue
		}
		begin := max(n.cursor, base)
		if begin >= until {
			continue
		}
		// A candidate beginning before until has complete right boundary context.
		// needleMatchesVisit is shared with whole-input screening; its cursor and
		// occurrence count live across windows in this adapter.
		state := needleCursor{byteAt: int(begin - base), occurrences: n.occurrences, ctx: ctx}
		transient := int64(len(n.needle.plain))*96 + 1024
		if err := hits.memory.take(transient); err != nil {
			return err
		}
		err := m.needleMatchesVisit(text, n.needle, n.value, &state, int(until-base), func(hit Match, start, end int) error {
			return hits.add(streamMatch{hit: hit, start: base + int64(start), end: base + int64(end)})
		})
		hits.memory.release(transient)
		if err != nil {
			return err
		}
		n.cursor = base + int64(state.byteAt)
		n.occurrences = state.occurrences
		if !eof && n.cursor < until {
			n.cursor = until
		}
	}
	return nil
}

// streamCatalog calls only the selected bounded detector entry point. Its meter
// borrows from the same pool as retained input and pending evidence.
func (m *Matcher) streamCatalog(ctx context.Context, text string, base int64, memory *streamMemory, hits *streamHits, work int64) error {
	if m.inert || m.detector == nil {
		return nil
	}
	var borrowed int64
	reserve := func(n int64) error {
		if err := memory.take(n); err != nil {
			return err
		}
		borrowed += n
		return nil
	}
	defer func() { memory.release(borrowed) }()
	findings, err := m.boundedFindings(ctx, text, memory.available(), work, reserve, func(n int64) { borrowed -= n; memory.release(n) })
	if err != nil {
		return err
	}
	// This index is independent of the finding arrays, so admit it before growth.
	indexBytes := int64(strings.Count(text, "\n")+1)*32 + 128
	if err := memory.take(indexBytes); err != nil {
		return err
	}
	defer memory.release(indexBytes)
	lineIdx := buildLineIndex(text)
	for i := range findings {
		if err := ctx.Err(); err != nil {
			return err
		}
		// GenericShape can inspect all matched runes even though its output is short.
		transient := int64(len(findings[i].Secret)+len(findings[i].Match))*96 + 1024
		if err := memory.take(transient); err != nil {
			return err
		}
		hit, ok := findingToMatch(text, &findings[i], lineIdx, m)
		memory.release(transient)
		if ok {
			offsets := MatchByteSpans(text, []Match{hit})
			if len(offsets) == 1 {
				if err := hits.add(streamMatch{hit, base + int64(offsets[0].Start), base + int64(offsets[0].End)}); err != nil {
					return err
				}
			}
		}
		findings[i].Secret = ""
		findings[i].Match = ""
		findings[i].Line = ""
		findings[i].Fragment = nil
	}
	return nil
}
