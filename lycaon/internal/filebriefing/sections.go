package filebriefing

import (
	"regexp"
	"strings"
)

var sectionLabel = regexp.MustCompile(`(?i)^(?:[-*•]\s+|\d+\.\s+|#+\s+)*(?:\*\*|__|\*)?(purpose|structure|key[ \t]+behavior)(?::(?:\*\*|__|\*)*|(?:\*\*|__|\*)+:)\s*(.*)$`)

var sectionKinds = map[string]string{
	"purpose":      "purpose",
	"structure":    "structure",
	"key behavior": "key_behavior",
}

var sectionOrder = []string{"purpose", "structure", "key_behavior"}

func ParseSections(summary string) []Section {
	sections := make([]Section, 0, 3)
	seenKinds := make(map[string]bool, len(sectionOrder))
	for _, raw := range strings.Split(summary, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if kind, text, ok := labeledSectionLine(line); ok {
			if seenKinds[kind] {
				return nil
			}
			seenKinds[kind] = true
			sections = append(sections, Section{Kind: kind, Text: text})
			continue
		}
		if len(sections) == 0 {
			continue
		}
		text := trimSectionBody(line)
		if text == "" {
			continue
		}
		last := &sections[len(sections)-1]
		if last.Text == "" {
			last.Text = text
		} else {
			last.Text += " " + text
		}
	}
	kept := sections[:0]
	for _, section := range sections {
		if section.Text != "" {
			kept = append(kept, section)
		}
	}
	if len(kept) != len(sectionOrder) {
		return nil
	}
	byKind := make(map[string]Section, len(kept))
	for _, section := range kept {
		byKind[section.Kind] = section
	}
	ordered := make([]Section, 0, len(sectionOrder))
	for _, kind := range sectionOrder {
		section, exists := byKind[kind]
		if !exists {
			return nil
		}
		ordered = append(ordered, section)
	}
	return ordered
}

func labeledSectionLine(line string) (kind, text string, ok bool) {
	match := sectionLabel.FindStringSubmatch(line)
	if match == nil {
		return "", "", false
	}
	kind, ok = sectionKinds[strings.Join(strings.Fields(strings.ToLower(match[1])), " ")]
	if !ok {
		return "", "", false
	}
	return kind, trimSectionBody(match[2]), true
}

func trimSectionBody(text string) string {
	text = strings.TrimSpace(text)
	for _, wrap := range []string{"**", "__", "*"} {
		text = strings.TrimPrefix(text, wrap)
		text = strings.TrimSuffix(text, wrap)
	}
	return strings.TrimSpace(text)
}
