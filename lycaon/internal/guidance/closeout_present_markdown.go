package guidance

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
)

// PresentMarkdownEmbedCode marks visual IDs embedded directly in markdown.
const PresentMarkdownEmbedCode = "PRESENT_MARKDOWN_EMBED"

// PresentImageUnobservedCode marks an image whose source is neither a visual
// artifact nor a URL the evidence observed: a remote or data URL the body
// presents as if it were a capture.
const PresentImageUnobservedCode = "PRESENT_IMAGE_UNOBSERVED"

// closeoutArtifactUUID matches an artifact store id.
var closeoutArtifactUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// HTML img with its source.
var closeoutHTMLImage = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>`)

// closeoutImageEmbed is one image the document renders: its syntax and source.
type closeoutImageEmbed struct {
	span   citationmd.Span
	source string
}

// closeoutImageEmbeds reads image nodes and raw HTML from the parse, so a literal
// example in code is no embed.
func closeoutImageEmbeds(doc citationmd.Document) []closeoutImageEmbed {
	out := make([]closeoutImageEmbed, 0, len(doc.Images))
	for _, image := range doc.Images {
		out = append(out, closeoutImageEmbed{span: image.Span, source: strings.TrimSpace(image.Destination)})
	}
	for _, zone := range doc.Zones {
		if zone.Kind != citationmd.ZoneRawHTML && zone.Kind != citationmd.ZoneHTMLBlock {
			continue
		}
		text := doc.ZoneText(zone)
		for _, m := range closeoutHTMLImage.FindAllStringSubmatchIndex(text, -1) {
			source := ""
			for group := 2; group < len(m) && source == ""; group += 2 {
				if m[group] >= 0 {
					source = text[m[group]:m[group+1]]
				}
			}
			out = append(out, closeoutImageEmbed{
				span:   citationmd.Span{Start: zone.Span.Start + m[0], End: zone.Span.Start + m[1]},
				source: strings.TrimSpace(source),
			})
		}
	}
	slices.SortFunc(out, func(a, b closeoutImageEmbed) int { return a.span.Start - b.span.Start })
	return out
}

// distinctSources lists the embeds' sources once each, in order.
func distinctSources(embeds []closeoutImageEmbed, keep func(string) bool) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, embed := range embeds {
		if embed.source == "" || !keep(embed.source) {
			continue
		}
		key := strings.ToLower(embed.source)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, embed.source)
	}
	return out
}

// CloseoutMarkdownArtifactEmbedIDs returns visual artifact UUIDs embedded as image
// sources, each once. Empty when the present path is clean.
func CloseoutMarkdownArtifactEmbedIDs(doc citationmd.Document) []string {
	return distinctSources(closeoutImageEmbeds(doc), closeoutArtifactUUID.MatchString)
}

// CloseoutUnobservedImageSources returns the image sources that are neither
// artifact ids nor URLs observed reports true for.
func CloseoutUnobservedImageSources(doc citationmd.Document, observed func(string) bool) []string {
	return distinctSources(closeoutImageEmbeds(doc), func(source string) bool {
		return !closeoutArtifactUUID.MatchString(source) && (observed == nil || !observed(source))
	})
}

// Collapse leftover blank lines from removed image-only paragraphs.
var closeoutEmbedBlankLines = regexp.MustCompile(`\n{3,}`)

// StripCloseoutImageEmbeds removes the image embeds the host cannot present from
// host text: bare artifact ids, whose ids are returned for binding, and sources
// observed does not vouch for. Observed images and literal examples stay.
func StripCloseoutImageEmbeds(synthesis string, observed func(string) bool) (string, []string) {
	doc := citationmd.Parse(synthesis, citationmd.Options{})
	embeds := closeoutImageEmbeds(doc)
	out := synthesis
	stripped := false
	for i := len(embeds) - 1; i >= 0; i-- {
		embed := embeds[i]
		if observed != nil && observed(embed.source) && !closeoutArtifactUUID.MatchString(embed.source) {
			continue
		}
		out = out[:embed.span.Start] + out[embed.span.End:]
		stripped = true
	}
	if !stripped {
		return synthesis, nil
	}
	out = closeoutEmbedBlankLines.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out), CloseoutMarkdownArtifactEmbedIDs(doc)
}
