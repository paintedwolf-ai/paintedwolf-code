package mutation_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Each redirect's receipt names the file as the model wrote it; its reviewed
// inputs keep the resolved path.
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

// Every row a redirect anchors is named by the file it wrote, never by the
// producer feeding it or a host path.
func TestRedirectRowsNameTheirFiles(t *testing.T) {
	f := mutationProgramFixture(t)
	testutil.FailErr(t, "record checklist", f.SetProgress("- [ ] Write the files"))
	for _, tc := range []struct {
		source string
		paths  []string
	}{
		{"printf 'x\\n' > one.txt", []string{"one.txt"}},
		{"printf 'a\\n' > first.txt && printf 'b\\n' > second.txt", []string{"first.txt", "second.txt"}},
		{"cat <<'EOF' > a.txt\na\nEOF\ncat <<'EOF' > b.txt\nb\nEOF", []string{"a.txt", "b.txt"}},
	} {
		raw, err := f.Call(t, t.Context(), tc.source)
		testutil.FailErr(t, "run writes", err)
		outcome := decodeMutationOutcome(t, raw)
		if outcome.Refusal != nil || outcome.Status != 0 {
			t.Fatalf("%q refused: %+v", tc.source, outcome)
		}
		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 100)
		testutil.FailErr(t, "read receipt", err)
		publications := map[string]bool{}
		for _, execution := range page.Executions {
			publications[execution.ID] = execution.EffectMode == "publication"
		}
		var named []string
		for _, activity := range page.Activities {
			if publications[activity.ID] {
				named = append(named, activity.Subject)
			}
		}
		if !slices.Equal(named, tc.paths) {
			t.Fatalf("%q redirect rows = %v, want %v", tc.source, named, tc.paths)
		}
	}
}
