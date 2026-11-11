package guard_test

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
)

// coordinatorReportMarkdown writes a closeout the way a model is asked to.
func coordinatorReportMarkdown(synthesis string, citedEvidence []guidance.CoordinatorCitedEvidence, citedURLs ...string) string {
	var b strings.Builder
	b.WriteString(synthesis)
	for _, cited := range citedEvidence {
		fmt.Fprintf(&b, " [%s:%d]", cited.Path, cited.Line)
	}
	for _, url := range citedURLs {
		fmt.Fprintf(&b, " <%s>", url)
	}
	return b.String()
}
