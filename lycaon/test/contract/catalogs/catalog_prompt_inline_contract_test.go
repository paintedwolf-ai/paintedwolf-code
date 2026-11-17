package contract

import (
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const untrustedOutputPartial = "lycaon/config/packs/painted-wolf/web-research/shared/partials/untrusted-output.md"

// Retrieval bodies and prompts carry the same trust notice.
func TestUntrustedTeachingMatchesPartial(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	partial := strings.TrimSpace(contractcheck.ReadRepoFile(t, root, untrustedOutputPartial))
	if partial == "" {
		t.Fatalf("%s is empty", untrustedOutputPartial)
	}
	source := contractcheck.ReadRepoFile(t, root, "lycaon/internal/datamark/datamark.go")
	if !strings.Contains(source, strconv.Quote(partial)) {
		t.Fatalf("datamark.go no longer carries the untrusted-output partial verbatim.\n"+
			"The constant is rendered into every framed retrieval body while the partial is rendered "+
			"into the prompt, so an agent that reads one and not the other gets different teaching.\n"+
			"partial: %s\nExpected a Go literal equal to: %q", untrustedOutputPartial, partial)
	}
}
