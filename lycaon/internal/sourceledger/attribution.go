package sourceledger

import (
	"strings"

	"github.com/aymanbagabas/go-udiff/lcs"
)

// Interval is one [start,end] inclusive 1-based line attribution span.
type Interval struct {
	Start    int
	End      int
	ChangeID string
}

// ApplyAttribution transfers surviving lines and attributes insertions.
func ApplyAttribution(existing []Interval, before, after, changeID string) []Interval {
	beforeLines, afterLines := splitLines(before), splitLines(after)
	tokens := lineTokens{ids: make(map[string]rune)}
	edits := lcs.DiffRunes(tokens.of(beforeLines), tokens.of(afterLines))
	cursor := attributionCursor{intervals: NormalizeIntervals(existing, len(beforeLines))}
	var out []Interval
	beforeAt, afterAt := 0, 0
	for _, edit := range edits {
		out = cursor.carry(out, beforeAt, edit.Start, afterAt)
		if edit.ReplEnd > edit.ReplStart && changeID != "" {
			out = append(out, Interval{Start: edit.ReplStart + 1, End: edit.ReplEnd, ChangeID: changeID})
		}
		beforeAt, afterAt = edit.End, edit.ReplEnd
	}
	out = cursor.carry(out, beforeAt, len(beforeLines), afterAt)
	return NormalizeIntervals(out, len(afterLines))
}

// attributionCursor advances ordered attribution intervals through unchanged line ranges.
type attributionCursor struct {
	intervals []Interval
	index     int
}

func (c *attributionCursor) carry(out []Interval, from, to, after int) []Interval {
	if from == to {
		return out
	}
	for c.index < len(c.intervals) {
		iv := c.intervals[c.index]
		if iv.Start > to {
			break
		}
		if iv.End > from {
			out = append(out, Interval{
				Start:    max(iv.Start, from+1) + after - from,
				End:      min(iv.End, to) + after - from,
				ChangeID: iv.ChangeID,
			})
		}
		if iv.End > to {
			break
		}
		c.index++
	}
	return out
}

// NormalizeIntervals merges adjacent same-id spans and drops empties/overlaps.
func NormalizeIntervals(ivs []Interval, fileLen int) []Interval {
	if len(ivs) == 0 {
		return nil
	}
	out := make([]Interval, 0, len(ivs))
	for _, iv := range ivs {
		if iv.Start < 1 {
			iv.Start = 1
		}
		if fileLen > 0 && iv.End > fileLen {
			iv.End = fileLen
		}
		if iv.End < iv.Start || iv.ChangeID == "" {
			continue
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.ChangeID == iv.ChangeID && iv.Start <= last.End+1 {
				if iv.End > last.End {
					last.End = iv.End
				}
				continue
			}
			if iv.Start <= last.End {
				iv.Start = last.End + 1
				if iv.End < iv.Start {
					continue
				}
			}
		}
		out = append(out, iv)
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// lineTokens gives each distinct line one rune so the line diff runs on runes.
type lineTokens struct {
	ids  map[string]rune
	last rune
}

func (t *lineTokens) of(lines []string) []rune {
	out := make([]rune, len(lines))
	for i, line := range lines {
		id, ok := t.ids[line]
		if !ok {
			t.last++
			id = t.last
			t.ids[line] = id
		}
		out[i] = id
	}
	return out
}
