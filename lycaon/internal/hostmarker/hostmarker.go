// Package hostmarker defines host-written transcript markers shared with the client.
package hostmarker

import "regexp"

const (
	EvidenceHandlePattern = `(?:[a-zA-Z0-9_-]+:)*[a-z_]+#[1-9][0-9]*`

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

var evidenceHandleTag = regexp.MustCompile(`^\[` + EvidenceHandlePattern + `\]\n?`)

// SplitEvidenceHandleTag preserves the optional leading handle tag.
func SplitEvidenceHandleTag(content string) (prefix, body string) {
	match := evidenceHandleTag.FindStringIndex(content)
	if match == nil {
		return "", content
	}
	return content[:match[1]], content[match[1]:]
}
