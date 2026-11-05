package guidance

import (
	"regexp"
	"strings"
)

// PresentMarkdownEmbedCode marks visual IDs embedded directly in markdown.
const PresentMarkdownEmbedCode = "PRESENT_MARKDOWN_EMBED"

// PresentImageUnobservedCode marks an image whose source is neither a visual
// artifact nor a URL the evidence observed: a remote or data URL the body
// presents as if it were a capture.
const PresentImageUnobservedCode = "PRESENT_IMAGE_UNOBSERVED"

// closeoutArtifactUUID matches an artifact store id.
var closeoutArtifactUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Inline markdown image with its source (optional title).
var closeoutMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\(\s*(<[^>]*>|[^\s)]+)\s*(?:["'][^"']*["'])?\s*\)`)

// HTML img with its source.
var closeoutHTMLImage = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>`)

// closeoutImageEmbed is one image the body embeds: the matched syntax and
// the source it names.
type closeoutImageEmbed struct {
	match  string
	source string
}

func closeoutImageEmbeds(synthesis string) []closeoutImageEmbed {
	synthesis = strings.TrimSpace(synthesis)
	if synthesis == "" {
		return nil
	}
	var out []closeoutImageEmbed
	for _, m := range closeoutMarkdownImage.FindAllStringSubmatch(synthesis, -1) {
		out = append(out, closeoutImageEmbed{match: m[0], source: strings.Trim(strings.TrimSpace(m[1]), "<>")})
	}
	for _, m := range closeoutHTMLImage.FindAllStringSubmatch(synthesis, -1) {
		source := m[1]
		if source == "" {
			source = m[2]
		}
		if source == "" {
			source = m[3]
		}
		out = append(out, closeoutImageEmbed{match: m[0], source: strings.TrimSpace(source)})
	}
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

// CloseoutMarkdownArtifactEmbedIDs returns visual artifact UUIDs embedded as
// markdown or HTML image sources in synthesis. Empty when present path is clean.
func CloseoutMarkdownArtifactEmbedIDs(synthesis string) []string {
	return distinctSources(closeoutImageEmbeds(synthesis), closeoutArtifactUUID.MatchString)
}

// CloseoutUnobservedImageSources returns the image sources in synthesis that
// are neither artifact ids nor URLs observed reports true for.
func CloseoutUnobservedImageSources(synthesis string, observed func(string) bool) []string {
	return distinctSources(closeoutImageEmbeds(synthesis), func(source string) bool {
		return !closeoutArtifactUUID.MatchString(source) && (observed == nil || !observed(source))
	})
}

// Collapse leftover blank lines from removed image-only paragraphs.
var closeoutEmbedBlankLines = regexp.MustCompile(`\n{3,}`)

// StripCloseoutImageEmbeds removes the image embeds the host cannot present:
// bare artifact ids, whose ids are returned for binding to artifact_ids, and
// sources observed does not vouch for. Observed images stay.
func StripCloseoutImageEmbeds(synthesis string, observed func(string) bool) (string, []string) {
	embeds := closeoutImageEmbeds(synthesis)
	ids := distinctSources(embeds, closeoutArtifactUUID.MatchString)
	out := synthesis
	stripped := false
	for _, embed := range embeds {
		if observed != nil && observed(embed.source) && !closeoutArtifactUUID.MatchString(embed.source) {
			continue
		}
		out = strings.ReplaceAll(out, embed.match, "")
		stripped = true
	}
	if !stripped {
		return synthesis, nil
	}
	out = closeoutEmbedBlankLines.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out), ids
}
