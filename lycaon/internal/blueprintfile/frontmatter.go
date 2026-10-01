package blueprintfile

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// SplitMarkdownFrontmatter separates optional YAML frontmatter from markdown body.
// Missing or unclosed fences are treated as no frontmatter — the full text is body.
// Corrupt YAML between valid fences yields empty meta and the body after the close fence.
func SplitMarkdownFrontmatter(content string) (meta map[string]any, body string) {
	content = strings.TrimPrefix(content, "\ufeff")
	raw, rest, ok := splitFrontmatterRaw(content)
	if !ok {
		return nil, content
	}
	meta = map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return meta, rest
	}
	if err := yaml.Unmarshal([]byte(raw), &meta); err != nil || meta == nil {
		return map[string]any{}, rest
	}
	return meta, rest
}

// splitFrontmatterRaw finds a closed --- … --- fence whose open/close lines are exactly "---".
// Unclosed opening fences (e.g. a markdown HR) are not treated as frontmatter.
func splitFrontmatterRaw(content string) (rawMeta, body string, ok bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", content, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		rawMeta = strings.Join(lines[1:i], "\n")
		body = strings.Join(lines[i+1:], "\n")
		return rawMeta, body, true
	}
	return "", content, false
}
