package promptattach

import (
	"fmt"
	"strings"
)

// SubjectBindingSource stamps the host subject-binding ContentPart.
const SubjectBindingSource = "attachment_subject_binding"

// maxSubjectNames caps names listed in SubjectBindingNotice.
const maxSubjectNames = 8

// SubjectBindingNotice names the turn's attached subjects.
func SubjectBindingNotice(sources []string) string {
	names := uniqueDisplayNames(sources)
	head := "This turn includes user attachment(s)"
	target := "those materials"
	if len(names) > 0 {
		target = "those named materials"
		listed := names
		suffix := ""
		if len(listed) > maxSubjectNames {
			extra := len(listed) - maxSubjectNames
			listed = listed[:maxSubjectNames]
			suffix = fmt.Sprintf(" (+%d more)", extra)
		}
		head = fmt.Sprintf("%s: %s%s", head, strings.Join(listed, ", "), suffix)
	}
	return fmt.Sprintf(
		"%s. Apply the user's request to %s first. Switch subject only when the user names another path.",
		head,
		target,
	)
}

func uniqueDisplayNames(sources []string) []string {
	seen := make(map[string]struct{}, len(sources))
	out := make([]string, 0, len(sources))
	for _, raw := range sources {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
