package guidance

import (
	"regexp"
	"strings"
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

// CloseoutMarkdownArtifactEmbedIDs returns visual artifact UUIDs embedded as
// markdown or HTML image sources in synthesis. Empty when present path is clean.
func CloseoutMarkdownArtifactEmbedIDs(synthesis string) []string {
	synthesis = strings.TrimSpace(synthesis)
	if synthesis == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || !closeoutArtifactUUID.MatchString(id) {
			return
		}
		key := strings.ToLower(id)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	for _, m := range closeoutMarkdownArtifactImage.FindAllStringSubmatch(synthesis, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range closeoutHTMLArtifactImage.FindAllStringSubmatch(synthesis, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	return ids
}

// Collapse leftover blank lines from removed image-only paragraphs.
var closeoutEmbedBlankLines = regexp.MustCompile(`\n{3,}`)

// StripCloseoutMarkdownArtifactEmbeds removes bare-UUID image embeds from synthesis
// and returns the ids found (for host-assemble present binding).
func StripCloseoutMarkdownArtifactEmbeds(synthesis string) (string, []string) {
	ids := CloseoutMarkdownArtifactEmbedIDs(synthesis)
	if len(ids) == 0 {
		return synthesis, nil
	}
	out := closeoutMarkdownArtifactImage.ReplaceAllString(synthesis, "")
	out = closeoutHTMLArtifactImage.ReplaceAllString(out, "")
	out = closeoutEmbedBlankLines.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out), ids
}
