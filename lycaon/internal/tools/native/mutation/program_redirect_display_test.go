package mutation_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Each redirect's receipt names the file as the model wrote it, never the
// host path its reviewed inputs resolve to.
func TestRedirectReceiptsNameTheWorkspacePath(t *testing.T) {
	f := mutationProgramFixture(t)
	testutil.FailErr(t, "record checklist", f.SetProgress("- [ ] Write the files"))
	for _, tc := range []struct{ operator, path string }{
		{">", "index.html"}, {">>", "log.txt"}, {"&>", "both.txt"}, {"&>>", "both.txt"},
	} {
		t.Run(tc.operator, func(t *testing.T) {
			raw, err := f.Call(t, t.Context(), "printf 'x\\n' "+tc.operator+" "+tc.path)
			testutil.FailErr(t, "run redirect", err)
			outcome := decodeMutationOutcome(t, raw)
			if outcome.Refusal != nil || outcome.Status != 0 {
				t.Fatalf("redirect refused: %+v", outcome)
			}
			page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 100)
			testutil.FailErr(t, "read receipt", err)
			redirects := 0
			for _, execution := range page.Executions {
				if strings.Contains(execution.Command, f.Dir) {
					t.Fatalf("receipt names the host path: %q", execution.Command)
				}
				if execution.EffectMode == "publication" {
					redirects++
					if execution.Command != tc.operator+" "+tc.path {
						t.Fatalf("redirect receipt = %q, want %q", execution.Command, tc.operator+" "+tc.path)
					}
				}
			}
			if redirects != 1 {
				t.Fatalf("redirect receipts = %d: %+v", redirects, page.Executions)
			}
		})
	}
}

// File writes are named by the files they wrote; the producer feeding a
// redirect is a step, never the row's subject.
func TestRedirectedWritesNameTheirFiles(t *testing.T) {
	f := mutationProgramFixture(t)
	testutil.FailErr(t, "record checklist", f.SetProgress("- [ ] Write the files"))
	editing := func(t *testing.T, source string) api.CommandActivity {
		t.Helper()
		raw, err := f.Call(t, t.Context(), source)
		testutil.FailErr(t, "run writes", err)
		outcome := decodeMutationOutcome(t, raw)
		if outcome.Refusal != nil || outcome.Status != 0 {
			t.Fatalf("writes refused: %+v", outcome)
		}
		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 100)
		testutil.FailErr(t, "read receipt", err)
		publications := map[string]bool{}
		for _, execution := range page.Executions {
			publications[execution.ID] = execution.EffectMode == "publication"
		}
		if len(page.Activities) != 1 || !publications[page.Activities[0].ID] {
			t.Fatalf("writes did not fold under a redirect: %+v", page.Activities)
		}
		return page.Activities[0]
	}

	if got := editing(t, "printf 'x\\n' > one.txt"); got.Subject != "one.txt" {
		t.Fatalf("single write subject = %q", got.Subject)
	}
	multi := editing(t, "printf 'a\\n' > index.html && printf 'b\\n' > style.css")
	if !strings.HasPrefix(multi.Subject, "index.html") || multi.Subject == "index.html" {
		t.Fatalf("multi-file subject = %q, want the first file and a summary of the rest", multi.Subject)
	}
	heredocs := editing(t, "cat <<'EOF' > a.html\na\nEOF\ncat <<'EOF' > b.css\nb\nEOF\ncat <<'EOF' > c.js\nc\nEOF")
	if !strings.HasPrefix(heredocs.Subject, "a.html") || heredocs.Subject == "a.html" || len(heredocs.Steps) < 6 {
		t.Fatalf("heredoc writes = subject %q, %d steps", heredocs.Subject, len(heredocs.Steps))
	}
}
