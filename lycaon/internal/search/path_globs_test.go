package search

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPathGlobFilter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags MatchFlags
		path  string
		want  bool
	}{
		{"unfiltered", MatchFlags{}, "target/generated.go", true},
		{"include", MatchFlags{Include: []string{"**/*.{go,ts}"}}, "target/generated.go", true},
		{"include miss", MatchFlags{Include: []string{"*.go"}}, "readme.md", false},
		{"exclude wins", MatchFlags{Include: []string{"*.go"}, Exclude: []string{"target/**"}}, "target/generated.go", false},
		{"brace exclusion", MatchFlags{Exclude: []string{"**/*.{go,ts}"}}, "src/a.ts", false},
		{"blank entries", MatchFlags{Include: []string{" "}, Exclude: []string{""}}, "src/a.go", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := compilePathGlobs(tc.flags)
			testutil.FailErr(t, "compile path globs", err)
			if got := filter.allows(tc.path); got != tc.want {
				t.Fatalf("allows %q = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestInvalidPathGlobsRejectBeforeSearchOrReplace(t *testing.T) {
	for _, flags := range []MatchFlags{
		{Include: []string{"["}},
		{Exclude: []string{"*.{go"}},
	} {
		for name, run := range map[string]func() error{
			"store": func() error {
				ctx := testCompileContext()
				ctx.Flags = flags
				_, err := CompileQuery("kind:web", ctx)
				return err
			},
			"code": func() error {
				_, err := NewCodeExecutor(decide.Reranker{}).Run(t.Context(), PlanLeg{Code: &CodePlanLeg{
					Query: TextExpr{Text: "hit"}, Lines: true, Flags: flags,
				}})
				return err
			},
			"preview": func() error {
				_, err := settledReplacePreview(t.Context(), ReplacePreviewRequest{Query: TextExpr{Text: "hit"}, Flags: flags})
				return err
			},
			"plan": func() error {
				_, err := PlanReplace(ReplacePlanRequest{
					Query: TextExpr{Text: "hit"}, Flags: flags, Store: rejectReadStore{t: t},
					Files: []ReplaceApplyFile{{Path: "src/a.go"}},
				})
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				var matchErr *MatchError
				if err := run(); !errors.As(err, &matchErr) {
					t.Fatalf("expected MatchError, got %v", err)
				}
			})
		}
	}
}

type rejectReadStore struct{ t *testing.T }

func (s rejectReadStore) ReadReplaceContent(string, string) (ReplaceContentRead, error) {
	s.t.Error("invalid glob reached file reading")
	return ReplaceContentRead{}, ErrReplaceNotFound
}
