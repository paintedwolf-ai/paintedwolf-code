package blueprintfile

import (
	"fmt"
	"strings"
)

// PlaceholderTitle is the host-minted display title when no name has been declared.
const PlaceholderTitle = "blueprint"

const placeholderPlanTitle = "plan"

// IsPlaceholderTitle reports whether title is still the host mint marker.
func IsPlaceholderTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", PlaceholderTitle, placeholderPlanTitle:
		return true
	default:
		return false
	}
}

// DeclaredTitle returns a non-placeholder frontmatter title.
func DeclaredTitle(content string) (string, bool) {
	meta, _ := SplitMarkdownFrontmatter(content)
	if meta == nil {
		return "", false
	}
	raw, ok := meta["title"]
	if !ok || raw == nil {
		return "", false
	}
	title := strings.TrimSpace(fmt.Sprint(raw))
	if IsPlaceholderTitle(title) {
		return "", false
	}
	return title, true
}
