package logview

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// RenderTurnDiff shows what changed in an agent's prompt from the previous turn to
// turn turnIndex. Because each turn's prompt is cumulative, the diff isolates what
// was actually added (a tool result, a host nudge, a rejection) plus any change to
// the rendered system layers.
func (d Display) RenderTurnDiff(w io.Writer, a *Agent, turnIndex int) {
	if a == nil || turnIndex <= 0 || turnIndex >= len(a.Turns) {
		fmt.Fprintln(w, d.Dim("no previous turn to diff against"))
		return
	}
	prev := d.plainPromptText(a.Turns[turnIndex-1], turnIndex)
	cur := d.plainPromptText(a.Turns[turnIndex], turnIndex+1)

	fmt.Fprintf(w, "%s\n\n", d.Bold(fmt.Sprintf("diff: turn %d → turn %d", turnIndex, turnIndex+1)))
	unified := udiff.Unified(fmt.Sprintf("turn %d", turnIndex), fmt.Sprintf("turn %d", turnIndex+1), prev, cur)
	if strings.TrimSpace(unified) == "" {
		fmt.Fprintln(w, d.Dim("(no change)"))
		return
	}
	for _, line := range strings.Split(strings.TrimRight(unified, "\n"), "\n") {
		fmt.Fprintln(w, d.colorDiffLine(line))
	}
}

// plainPromptText renders a turn's transcript without color or wrapping, for a
// stable line-by-line diff.
func (d Display) plainPromptText(r LLMRecord, index int) string {
	plain := Display{UnescapeHTML: d.UnescapeHTML, TimeFormat: d.TimeFormat}
	var buf bytes.Buffer
	_ = plain.RenderPrompt(&buf, r, index, PromptOptions{})
	return buf.String()
}

func (d Display) colorDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return d.Dim(d.wrapDiff(line))
	case strings.HasPrefix(line, "@@"):
		return d.Cyan(d.wrapDiff(line))
	case strings.HasPrefix(line, "+"):
		return d.Green(d.wrapDiff(line))
	case strings.HasPrefix(line, "-"):
		return d.Red(d.wrapDiff(line))
	default:
		return d.Dim(d.wrapDiff(line))
	}
}

// wrapDiff soft-wraps an over-long diff line to the display width, keeping the
// +/-/@ marker and indenting continuations.
func (d Display) wrapDiff(line string) string {
	if d.Width <= 0 || len([]rune(line)) <= d.Width {
		return line
	}
	marker := line[:1]
	segs := wrapPlain(line[1:], d.Width-2)
	for i := range segs {
		if i == 0 {
			segs[i] = marker + segs[i]
		} else {
			segs[i] = marker + " " + segs[i]
		}
	}
	return strings.Join(segs, "\n")
}
