package promptrender_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestZZDiagCustomizedStanding(t *testing.T) {
	s, err := promptrender.LoadStock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	states, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.Excluded || !strings.Contains(st.ID, "inline-work") || !strings.Contains(st.ID, "dispatch-on-investigate") {
			continue
		}
		text, rErr := promptrender.RenderCoordinatorStandingForTest(t.Context(), s, st, "")
		i := strings.Index(text, "repo-wide codemod")
		snippet := ""
		if i >= 0 {
			snippet = text[i:min(len(text), i+260)]
		}
		t.Logf("DIAG %s err=%v dispatch=%t codemod=%q", st.ID, rErr, strings.Contains(text, "## Dispatch turn"), snippet)
	}
}
