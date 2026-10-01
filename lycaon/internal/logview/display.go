package logview

import (
	"encoding/json"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Display is the shared rendering context for both the CLI and the TUI. It bundles
// the color Palette with the cross-cutting presentation preferences sourced from
// config (HTML unescaping, time format) so every surface renders identically.
type Display struct {
	Palette
	UnescapeHTML bool
	TimeFormat   string // "clock" (default), "iso"
	// Width soft-wraps block content to this column count (0 = no wrap; the CLI
	// relies on the terminal, the TUI sets its viewport width so paging works).
	Width int
}

// WithWidth returns a copy of d that wraps block content to w columns.
func (d Display) WithWidth(w int) Display {
	d.Width = w
	return d
}

// indentWrap prefixes every logical line of content with gutter, soft-wrapping to
// the display width so the gutter is preserved on continuation lines. Wrapping is
// ANSI-aware, so syntax-highlighted content wraps without breaking color spans.
// head caps the number of source lines (0 = all).
func (d Display) indentWrap(content, gutter string, head int) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	truncated := 0
	if head > 0 && len(lines) > head {
		truncated = len(lines) - head
		lines = lines[:head]
	}
	avail := 0
	if gw := len([]rune(gutter)); d.Width > gw {
		avail = d.Width - gw
	}
	prefix := d.Dim(gutter)
	var out []string
	for _, ln := range lines {
		if avail > 0 {
			for _, seg := range strings.Split(ansi.Wrap(ln, avail, ""), "\n") {
				out = append(out, prefix+seg)
			}
		} else {
			out = append(out, prefix+ln)
		}
	}
	if truncated > 0 {
		out = append(out, prefix+"… +"+strconv.Itoa(truncated)+" more lines")
	}
	return strings.Join(out, "\n")
}

// hlJSON syntax-highlights a JSON block, honoring color/size limits.
func (d Display) hlJSON(s string) string { return d.highlight(s, "json") }

// hlAuto highlights JSON when the content looks like JSON, else returns it as-is.
func (d Display) hlAuto(s string) string {
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return d.hlJSON(s)
	}
	return s
}

func (d Display) highlight(s, lexer string) string {
	if !d.enabled || len(s) > maxHighlightBytes {
		return s
	}
	return highlightSource(s, lexer)
}

// NewDisplay builds a Display from a resolved Config and a color decision.
func NewDisplay(cfg Config, color bool) Display {
	return Display{
		Palette:      NewPalette(color),
		UnescapeHTML: cfg.UnescapeHTML,
		TimeFormat:   cfg.TimeFormat,
	}
}

func (d Display) time(ts time.Time) string {
	if ts.IsZero() {
		return "--:--:--.---"
	}
	if d.TimeFormat == "iso" {
		return ts.Local().Format("2006-01-02T15:04:05.000")
	}
	return ts.Local().Format("15:04:05.000")
}

// text renders a captured message Content as plain text, applying HTML unescaping
// when configured (worker digests arrive HTML-escaped: &#xA; &#34; …).
func (d Display) text(raw json.RawMessage) string {
	return d.plainText(messageText(raw))
}

// plainText applies the configured HTML unescaping to an already-plain string.
func (d Display) plainText(s string) string {
	if d.UnescapeHTML {
		return html.UnescapeString(s)
	}
	return s
}
