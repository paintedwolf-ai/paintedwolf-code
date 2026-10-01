package search

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReplacePreservesQuerySemantics(t *testing.T) {
	t.Run("newline_ending_regex", func(t *testing.T) {
		m, err := compileReplaceMatcher(TextExpr{Text: `\n`}, MatchFlags{Regex: true})
		testutil.FailErr(t, "compile newline", err)
		hunks := collectReplaceHunks("a\nb", m, " ")
		if len(hunks) != 1 || hunks[0].Before != "a\nb" || hunks[0].After != "a b" {
			t.Fatalf("newline preview = %+v", hunks)
		}
	})
	for _, tc := range []struct {
		name, pattern, content, replacement, want string
		flags                                     MatchFlags
	}{
		{"literal_dollar", "old", "old", "$1", "$1", MatchFlags{}},
		{"regex_boundary_context", `\Bcat`, "bobcat", "dog", "bobdog", MatchFlags{Regex: true}},
		{"regex_capture_context", `\B(c)(at)`, "bobcat bobcat", "$2$1", "bobatc bobatc", MatchFlags{Regex: true}},
		{"regex_zero_width", `^`, "a\nb", ">", ">a\n>b", MatchFlags{Regex: true}},
		{"trailing_newline", `\n`, "a\n", "", "a", MatchFlags{Regex: true}},
		{"newline_capture", `(a\n)`, "a\nb", "$1$1", "a\na\nb", MatchFlags{Regex: true}},
		{"literal_dollar_whole_word", "old", "old", "$0", "$0", MatchFlags{WholeWord: true}},
		{"quoted_spaces", " old ", "old old old", "X", "oldXold", MatchFlags{CaseSensitive: true}},
		{"quoted_asterisk", "*", "a*b\nuntouched", "X", "aXb\nuntouched", MatchFlags{CaseSensitive: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pattern, err := replacementPattern(TextExpr{Text: tc.pattern, Phrase: true})
			testutil.FailErr(t, "extract pattern", err)
			m, err := compileReplaceMatcher(pattern, tc.flags)
			testutil.FailErr(t, "compile pattern", err)
			hunks := collectReplaceHunks(tc.content, m, tc.replacement)
			got, ok := applyHunksToContent(tc.content, hunks)
			if !ok || got != tc.want {
				t.Errorf("content=%q pattern=%q replacement=%q got=%q want=%q", tc.content, tc.pattern, tc.replacement, got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, pattern, content string
		flags                  MatchFlags
	}{
		{"unicode_prefilter", "s", "ſ", MatchFlags{}},
		{"inline_fold_prefilter", `(?i)old`, "old", MatchFlags{Regex: true, CaseSensitive: true}},
		{"wholeword_literal_prefilter", `foo\nbar`, `foo\nbar`, MatchFlags{WholeWord: true, CaseSensitive: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := TextExpr{Text: tc.pattern}
			m, err := compileReplaceMatcher(term, tc.flags)
			testutil.FailErr(t, "compile prefilter matcher", err)
			matches := m.findAll(tc.content, "replacement")
			filter := compileReplacePrefilter(term, tc.flags)
			if len(matches) > 0 && filter.active() && !filter.matches([]byte(tc.content)) {
				t.Errorf("prefilter rejects content=%q despite matcher spans=%v", tc.content, matches)
			}
		})
	}
}

func TestReplacementScopeSurvivesPreviewAndApply(t *testing.T) {
	p, dir := replaceFixture(t)
	query, err := ParseQuery(`old path:a.go kind:code`)
	testutil.FailErr(t, "parse scoped query", err)
	preview, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query: query, Replacement: "new",
		Roots: []CodeRoot{{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: dir}},
	})
	testutil.FailErr(t, "preview scoped replacement", err)
	if len(preview.Files) != 1 || preview.Files[0].Path != "a.go" {
		t.Fatalf("scoped preview = %+v", preview)
	}
	store := testReplaceStore{project: p}
	b, err := store.ReadReplaceContent(p.Roots[0].ID, "b.go")
	testutil.FailErr(t, "read excluded file", err)
	result, err := PlanReplace(ReplacePlanRequest{
		Query: query, Replacement: "new", Store: store,
		Files: []ReplaceApplyFile{
			{RootID: p.Roots[0].ID, Path: "a.go", SHA256: preview.Files[0].SHA256, HunkIndexes: []int{0}},
			{RootID: p.Roots[0].ID, Path: "b.go", SHA256: b.SHA256, HunkIndexes: []int{0}},
		},
	})
	testutil.FailErr(t, "plan scoped replacement", err)
	if len(result.Writes) != 1 || result.Writes[0].Path != "a.go" || !result.Files[1].Skipped {
		t.Fatalf("scoped plan = %+v", result)
	}
}
