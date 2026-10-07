package progress

import "strings"

// SeedChecklist appends one pending row per label to a document with no checklist.
func SeedChecklist(content string, labels []string) string {
	body := strings.TrimRight(content, "\n")
	if strings.TrimSpace(body) == "" {
		body = strings.TrimRight(formatBootstrapContent(""), "\n")
	}
	var b strings.Builder
	b.WriteString(body)
	if !strings.Contains(body, "## Progress") {
		b.WriteString("\n\n## Progress")
	}
	for _, label := range labels {
		label = strings.Join(strings.Fields(label), " ")
		if label == "" {
			continue
		}
		b.WriteString("\n- [ ] " + label)
	}
	b.WriteString("\n")
	return b.String()
}
