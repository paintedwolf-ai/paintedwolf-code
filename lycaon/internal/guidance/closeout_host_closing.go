package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// HostClosingMarkdown is what the host says when a turn ended with no answer to
// deliver. It reports state and claims no finding.
func HostClosingMarkdown(cause string, ev evidence.Ledger, progress string) string {
	var b strings.Builder
	b.WriteString("This turn ended without a final answer, because " + strings.TrimSpace(cause) + ". Nothing below is a finding.")
	if paths := FormatOffenderReport(evidence.ObservedPathsSorted(ev)); paths.Count > 0 {
		b.WriteString("\n\nRead before it ended (" + paths.Sample + ").")
	} else {
		b.WriteString("\n\nNo file was read before it ended.")
	}
	if progress = strings.TrimSpace(progress); progress != "" {
		b.WriteString("\n\nChecklist as it stood:\n\n" + progress)
	}
	b.WriteString("\n\nAsk me to continue and I will pick up from here.")
	return b.String()
}
