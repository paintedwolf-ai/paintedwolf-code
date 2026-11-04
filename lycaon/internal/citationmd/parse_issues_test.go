package citationmd

import (
	"reflect"
	"strings"
	"testing"
)

// AC-AST-10, AC-AST-12: defects are explicit, never absorbed into acceptance.
func TestParseIssues(t *testing.T) {
	t.Parallel()
	reason := strings.Repeat("r", 2001)
	cases := []struct {
		name, source string
		want         IssueCode
		kind         NodeKind
	}{
		{"AC-AST-10 handle as method", "[verify: command#4 - ran]", IssueInvalidVerification, KindVerify},
		{"AC-AST-10 command text as method", "[verify: go test ./... - ran]", IssueInvalidVerification, KindVerify},
		{"unknown method", "[verify: vibes - ran]", IssueInvalidVerification, KindVerify},
		{"capitalized method", "[verify: Targeted - ran]", IssueInvalidVerification, KindVerify},
		{"missing separator", "[verify: targeted ran]", IssueInvalidVerification, KindVerify},
		{"empty reason", "[verify: targeted - ]", IssueInvalidVerification, KindVerify},
		{"reason over its bound", "[verify: targeted - " + reason + "]", IssueInvalidVerification, KindVerify},
		{"AC-AST-12 bad artifact", "[artifact: not-a-uuid]", IssueMalformedArtifact, KindArtifact},
		{"empty artifact", "[artifact:]", IssueMalformedArtifact, KindArtifact},
		{"AC-AST-12 bad repair", "[cite: nope]", IssueMalformedRepair, KindRepair},
		{"repair bad target", "[cite: x1 => read#1]", IssueMalformedRepair, KindRepair},
		{"repair bad value", "[cite: c1 => read#1 extra words]", IssueMalformedRepair, KindRepair},
		{"repair non-web url", "[cite: c1 => <ftp://example.test>]", IssueMalformedRepair, KindRepair},
		{"zero ordinal", "[read#0]", IssueMalformedCitation, KindCitation},
		{"AC-AST-12 handle-shaped junk", "[foo#bar]", IssueMalformedCitation, KindCitation},
		{"handle then prose", "[read#3 some words]", IssueMalformedCitation, KindCitation},
		{"bad coordinate", "[read#3:foo]", IssueMalformedCitation, KindCitation},
		{"coordinate twice", "[grep#1:2 a.go:3]", IssueMalformedCitation, KindCitation},
		{"bad quoted coordinate", `["a b.md":x]`, IssueMalformedCitation, KindCitation},
		{"empty quoted path", `[""]`, IssueMalformedCitation, KindCitation},
		{"bad quoted escape", `["a\qb"]`, IssueMalformedCitation, KindCitation},
		{"unknown name in a confirmed group", "[read#1, sic]", IssueMalformedCitation, KindCitation},
		{"unknown bare path after an attempt", "[a.go:1, sic]", IssueMalformedCitation, KindCitation},
		{"unknown bare path after a space", "[a.go:1 , b/c.go]", IssueMalformedCitation, KindCitation},
		{"zero line on a path", "[a.go:0]", IssueInvalidRange, KindCitation},
		{"zero line", "[read#3:0]", IssueInvalidRange, KindCitation},
		{"backward range", "[read#3:58-42]", IssueInvalidRange, KindCitation},
		{"backward range on a path", "[a/b.go:58-42]", IssueInvalidRange, KindCitation},
		{"AC-AST-17 unterminated verify", "[verify: targeted - x\nnext", IssueUnterminatedNode, KindVerify},
		{"unterminated handle at end", "[read#3", IssueUnterminatedNode, KindCitation},
		{"unterminated quote", `[read#3 "abc]`, IssueUnterminatedNode, KindCitation},
		{"unterminated artifact", "[artifact: x", IssueUnterminatedNode, KindArtifact},
		{"unterminated repair", "[cite: c1 =>", IssueUnterminatedNode, KindRepair},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, knownNames())
			if len(doc.Occurrences) != 0 || len(doc.Issues) != 1 {
				t.Fatalf("occurrences = %+v, issues = %+v", doc.Occurrences, doc.Issues)
			}
			if issue := doc.Issues[0]; issue.Code != tc.want || issue.Kind != tc.kind {
				t.Fatalf("issue = %+v, want %s/%s", issue, tc.want, tc.kind)
			}
			if got := Project(doc); strings.Contains(got, "verify:") || strings.Contains(got, "read#") || strings.Contains(got, "cite:") {
				t.Fatalf("damaged node reached display: %q", got)
			}
		})
	}
}

// AC-AST-10: a conflicting assessment is reported; a repeat is not.
func TestParseConflictingVerification(t *testing.T) {
	t.Parallel()
	same := Parse("[verify: targeted - a] [verify: targeted - a]", Options{})
	if len(same.Issues) != 0 {
		t.Fatalf("repeat issues = %+v", same.Issues)
	}
	conflict := Parse("[verify: targeted - a] [verify: project - b]", Options{})
	if got := issueCodes(conflict); !reflect.DeepEqual(got, []IssueCode{IssueConflictingVerification}) {
		t.Fatalf("conflict issues = %+v", conflict.Issues)
	}
	if conflict.Issues[0].Span.Start == 0 {
		t.Fatalf("conflict names the first assessment: %+v", conflict.Issues[0])
	}
}

func TestParseDirectives(t *testing.T) {
	t.Parallel()
	const a, b = "11111111-1111-1111-1111-111111111111", "AAAAAAAA-2222-2222-2222-222222222222"
	doc := Parse("[artifact: "+a+", "+b+"] [artifact:"+a+"]", Options{})
	if len(doc.Issues) != 0 || len(doc.Occurrences) != 3 {
		t.Fatalf("artifact parse = %+v / %+v", doc.Occurrences, doc.Issues)
	}
	if doc.Occurrences[0].Artifact != a || doc.Occurrences[1].Artifact != b || doc.Occurrences[1].Token != b {
		t.Fatalf("artifact members = %+v", doc.Occurrences[:2])
	}
	repairs := Parse(`[cite: c1 => read#5:42 "a]b"] [cite: c2 => <https://example.test/a]b>] [cite: c3 => Dockerfile:2]`, Options{})
	if len(repairs.Issues) != 0 || len(repairs.Occurrences) != 3 {
		t.Fatalf("repair parse = %+v / %+v", repairs.Occurrences, repairs.Issues)
	}
	first := repairs.Occurrences[0]
	if first.Kind != KindRepair || first.Repair.Target != "c1" || first.Repair.Citation.Handle != "read#5" || first.Repair.Citation.Excerpt != "a]b" {
		t.Fatalf("repair = %+v", first.Repair)
	}
	if second := repairs.Occurrences[1]; second.Repair.URL != "https://example.test/a]b" {
		t.Fatalf("repair = %+v", second.Repair)
	}
	if third := repairs.Occurrences[2]; third.Repair.Citation.Path != "Dockerfile" {
		t.Fatalf("repair = %+v", third.Repair)
	}
}

// AC-AST-12: budgets return a structured issue and never drop a reference into
// silent acceptance.
func TestParseBudgets(t *testing.T) {
	t.Parallel()
	doc := Parse("[read#1] [read#2] [a](https://example.test) [read#3]", Options{MaxOccurrences: 1})
	if got := issueCodes(doc); !reflect.DeepEqual(got, []IssueCode{IssueOccurrenceBudget}) || doc.Issues[0].Token != "3" {
		t.Fatalf("issues = %+v", doc.Issues)
	}
	if len(doc.Occurrences) != 1 {
		t.Fatalf("occurrences = %+v", doc.Occurrences)
	}
	if got := Project(doc); strings.Contains(got, "read#") {
		t.Fatalf("node past the budget reached display: %q", got)
	}
	huge := Parse("prose [read#1] prose", Options{MaxSourceBytes: 10})
	if got := issueCodes(huge); !reflect.DeepEqual(got, []IssueCode{IssueSourceBudget}) {
		t.Fatalf("issues = %+v", huge.Issues)
	}
	if got := Project(huge); got != "prose prose" {
		t.Fatalf("over-budget source presented %q", got)
	}
	long := boundedToken(strings.Repeat("é", 400))
	if len(long) > maxIssueTokenBytes || strings.ContainsRune(long, '\uFFFD') {
		t.Fatalf("bounded token = %q", long)
	}
}

// Sources built to make the block or link parser quadratic are not parsed at that
// cost: they report a structured issue and keep a display with no protocol nodes.
func TestParseStructureBudget(t *testing.T) {
	t.Parallel()
	nested := func(unit string, depth int) string { return strings.Repeat(unit, depth) + "x" }
	refused := []string{
		strings.Repeat("[a](", 1<<14),
		strings.Repeat("![a](", 1<<14),
		strings.Repeat("[read#1](", 1<<14),
		nested(">", maxContainerDepth+1),
		nested("> ", maxContainerDepth+1),
		nested("- ", maxContainerDepth+1),
		nested("* ", maxContainerDepth+1),
		nested("+ ", maxContainerDepth+1),
		nested("1. ", maxContainerDepth+1),
		nested("2) ", maxContainerDepth+1),
		"ok\n" + nested("> - ", maxContainerDepth),
	}
	for _, source := range refused {
		doc := Parse(source, Options{})
		if got := issueCodes(doc); len(got) == 0 || got[0] != IssueStructureBudget {
			t.Errorf("%.20q: issues = %+v", source, got)
		}
		// The issue blocks acceptance; a fallback still has the prose to show.
		if got := Project(doc); got == "" || strings.Contains(got, "read#") {
			t.Errorf("%.20q: refused source presented %.20q", source, got)
		}
	}
	allowed := []string{
		nested("> ", maxContainerDepth) + " [read#1]",
		strings.Repeat("> plain\n", maxContainerDepth+1) + "[read#1]",
		strings.Repeat("1234567890. no ", 8) + "[read#1]",
		"-x *y 1.z " + strings.Repeat("a [b](c) ", 900) + "[read#1]",
		strings.Repeat("*a_ a* ", 1<<13) + "[read#1]",
		strings.Repeat("   ", 100),
	}
	for _, source := range allowed {
		if doc := Parse(source, Options{}); len(doc.Issues) != 0 {
			t.Errorf("%.20q: issues = %+v", source, doc.Issues)
		}
	}
}

// A single token's cost is linear: an occurrence-dense source stays within its
// budget and records every node up to it.
func TestParseBoundedByNodeCount(t *testing.T) {
	t.Parallel()
	source := strings.Repeat("[read#1] ", MaxOccurrences+50)
	doc := Parse(source, Options{})
	if len(doc.Occurrences) != MaxOccurrences {
		t.Fatalf("occurrences = %d", len(doc.Occurrences))
	}
	if doc.Issues[0].Token != "50" {
		t.Fatalf("issue = %+v", doc.Issues[0])
	}
}
