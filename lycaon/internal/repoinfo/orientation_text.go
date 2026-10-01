package repoinfo

import (
	"strings"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

// FormatOrientationBriefText renders count-gated orientation lines for tool output.
func FormatOrientationBriefText(sections []api.BoardOrientationRoot) string {
	if len(sections) < 2 {
		var single api.RepoBrief
		if len(sections) == 1 {
			single = sections[0].Brief
		}
		return strings.Join(packboard.RepoOrientationLines(single), "\n")
	}
	var b strings.Builder
	for _, root := range sections {
		header := sectionHeader(root.Label, root.IsPrimary)
		b.WriteString(header)
		b.WriteByte('\n')
		for _, line := range packboard.RepoOrientationLines(root.Brief) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if root.Truncated {
			b.WriteString("(brief trimmed to fit budget)\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
