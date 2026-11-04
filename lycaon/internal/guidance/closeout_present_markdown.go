package guidance

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
)

// PresentMarkdownEmbedCode marks visual IDs embedded directly in markdown.
const PresentMarkdownEmbedCode = "PRESENT_MARKDOWN_EMBED"

// closeoutArtifactUUID matches an artifact store id.
var closeoutArtifactUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Inline markdown image whose href is only a UUID (optional title).
var closeoutMarkdownArtifactImage = regexp.MustCompile(
	`!\[[^\]]*\]\(\s*([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\s*(?:["'][^"']*["'])?\s*\)`,
)

// HTML img whose src is only a UUID.
var closeoutHTMLArtifactImage = regexp.MustCompile(
	`(?i)<img\b[^>]*\bsrc\s*=\s*["']?\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\s*["']?[^>]*>`,
)

// artifactEmbed is one image node whose source is an artifact UUID.
type artifactEmbed struct {
	span citationmd.Span
	id   string
}

// closeoutArtifactEmbeds reads image nodes from the parse, so a literal example in
// code is no embed.
func closeoutArtifactEmbeds(doc citationmd.Document) []artifactEmbed {
	var embeds []artifactEmbed
	for _, zone := range doc.Zones {
		var pattern *regexp.Regexp
		switch zone.Kind {
		case citationmd.ZoneImage:
			pattern = closeoutMarkdownArtifactImage
		case citationmd.ZoneRawHTML, citationmd.ZoneHTMLBlock:
			pattern = closeoutHTMLArtifactImage
		case citationmd.ZoneFence, citationmd.ZoneIndented, citationmd.ZoneDefinition, citationmd.ZoneCodeSpan, citationmd.ZoneLink, citationmd.ZoneAutolink:
			continue
		}
		text := doc.ZoneText(zone)
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			id := text[match[2]:match[3]]
			if closeoutArtifactUUID.MatchString(id) {
				embeds = append(embeds, artifactEmbed{
					span: citationmd.Span{Start: zone.Span.Start + match[0], End: zone.Span.Start + match[1]},
					id:   id,
				})
			}
		}
	}
	return embeds
}

// CloseoutMarkdownArtifactEmbedIDs returns visual artifact UUIDs embedded as image
// sources, each once. Empty when the present path is clean.
func CloseoutMarkdownArtifactEmbedIDs(doc citationmd.Document) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, embed := range closeoutArtifactEmbeds(doc) {
		key := strings.ToLower(embed.id)
		if _, dup := seen[key]; !dup {
			seen[key] = struct{}{}
			ids = append(ids, embed.id)
		}
	}
	return ids
}

// Collapse leftover blank lines from removed image-only paragraphs.
var closeoutEmbedBlankLines = regexp.MustCompile(`\n{3,}`)

// StripCloseoutMarkdownArtifactEmbeds removes bare-UUID image embeds from host text
// and returns the ids found, for host-assemble present binding.
func StripCloseoutMarkdownArtifactEmbeds(synthesis string) (string, []string) {
	doc := citationmd.Parse(synthesis, citationmd.Options{})
	embeds := closeoutArtifactEmbeds(doc)
	if len(embeds) == 0 {
		return synthesis, nil
	}
	ids := CloseoutMarkdownArtifactEmbedIDs(doc)
	out := synthesis
	for i := len(embeds) - 1; i >= 0; i-- {
		out = out[:embeds[i].span.Start] + out[embeds[i].span.End:]
	}
	out = closeoutEmbedBlankLines.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out), ids
}
