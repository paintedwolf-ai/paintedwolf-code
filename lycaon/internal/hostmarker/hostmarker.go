// Package hostmarker defines host-written transcript markers shared with the client.
package hostmarker

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	EvidenceHandlePattern = `(?:[a-zA-Z0-9_-]+:)*[a-z_]+#[1-9][0-9]*`
	// EvidenceCoveragePattern is the path and lines a source-delivery tag states.
	EvidenceCoveragePattern = `(?:"[^"\n\]]*"|[^\s\],"]+):[0-9]+(?:-[0-9]+)?`

	OverlayPromoteEventPrefix = "[host:overlay-promote-event]"
	OverlayRejectEventPrefix  = "[host:overlay-reject-event]"

	// CompactionBannerOpen prefixes a compacted tool result.
	CompactionBannerOpen = "[compacted "
	// CompactionBannerClose ends that banner.
	CompactionBannerClose = "]"

	// VerbatimHeadTail heads the excerpt a compacted chunk keeps intact.
	VerbatimHeadTail = "verbatim head/tail:"

	// GuidanceBlockOpen prefixes host guidance.
	GuidanceBlockOpen = ">>> "

	// Rejected heads a structured tool rejection; a CodeLine follows it.
	Rejected = "Rejected:"

	// CodeLine prefixes the machine-readable code inside a rejection or kick.
	CodeLine = "Code:"
)

// markers defines the generated client constants in emission order.
var markers = []Marker{
	{Name: "EVIDENCE_HANDLE_PATTERN", Value: EvidenceHandlePattern},
	{Name: "EVIDENCE_COVERAGE_PATTERN", Value: EvidenceCoveragePattern},
	{Name: "OVERLAY_PROMOTE_EVENT_PREFIX", Value: OverlayPromoteEventPrefix},
	{Name: "OVERLAY_REJECT_EVENT_PREFIX", Value: OverlayRejectEventPrefix},
	{Name: "COMPACTION_BANNER_OPEN", Value: CompactionBannerOpen},
	{Name: "COMPACTION_BANNER_CLOSE", Value: CompactionBannerClose},
	{Name: "VERBATIM_HEAD_TAIL", Value: VerbatimHeadTail},
	{Name: "GUIDANCE_BLOCK_OPEN", Value: GuidanceBlockOpen},
	{Name: "REJECTED", Value: Rejected},
	{Name: "CODE_LINE", Value: CodeLine},
}

// Marker is one host-written literal and the name its generated binding takes.
type Marker struct {
	Name  string
	Value string
}

// All returns a copy of the marker catalog.
func All() []Marker {
	return append([]Marker(nil), markers...)
}

// CompactionBanner composes the banner line for a compacted chunk.
func CompactionBanner(body string) string {
	return CompactionBannerOpen + body + CompactionBannerClose
}

var evidenceHandleTag = regexp.MustCompile(`^\[(` + EvidenceHandlePattern + `)(?: ` + EvidenceCoveragePattern + `)?\]\n?`)

// EvidenceTagHandle returns the handle of a leading tag, without its coverage.
func EvidenceTagHandle(tag string) string {
	match := evidenceHandleTag.FindStringSubmatch(tag)
	if match == nil {
		return ""
	}
	return match[1]
}

// FormatEvidenceCoverage states a delivery's path and lines in citation grammar, so
// a copied tag is a citation. A path the grammar cannot carry states nothing.
func FormatEvidenceCoverage(path string, start, end int) string {
	if path == "" || start < 1 || end < start || strings.ContainsAny(path, "]\"\n\r") {
		return ""
	}
	if strings.ContainsAny(path, " \t,") {
		path = `"` + path + `"`
	}
	if start == end {
		return fmt.Sprintf("%s:%d", path, start)
	}
	return fmt.Sprintf("%s:%d-%d", path, start, end)
}

// SplitEvidenceHandleTag preserves the optional leading handle tag.
func SplitEvidenceHandleTag(content string) (prefix, body string) {
	match := evidenceHandleTag.FindStringIndex(content)
	if match == nil {
		return "", content
	}
	return content[:match[1]], content[match[1]:]
}

// Parroted transport markers. Projection no longer writes them, but models that
// read older transcripts still copy them.
const parrotedDataLine = "⟦D⟧"

var parrotedDataHeaders = map[string]struct{}{
	"⟦D:host:internal_step_note⟧":  {},
	"⟦D:host:internal_step_note⟦":  {},
	"⟦D:model:internal_step_note⟧": {},
	"⟦D:model:internal_step_note⟦": {},
}

// StripParrotedTransportMarkers removes exact parroted header lines and leading
// data-line markers. It matches markers only, never sentences, and returns text
// holding no marker unchanged.
func StripParrotedTransportMarkers(text string) string {
	if !strings.Contains(text, "⟦D") {
		return text
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	kept := lines[:0]
	for _, line := range lines {
		for strings.HasPrefix(strings.TrimLeft(line, " \t"), parrotedDataLine) {
			line = strings.TrimPrefix(strings.TrimLeft(line, " \t"), parrotedDataLine)
		}
		if _, header := parrotedDataHeaders[strings.TrimSpace(line)]; header {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
