package citationmd

import (
	"reflect"
	"strings"
	"testing"
)

// AC-AST-15: a valid maximal assessment parses within the node cap.
func TestParseMaximalAssessment(t *testing.T) {
	t.Parallel()
	reason := strings.Repeat("reason ", 285) + "ends"
	source := "Before. [verify: targeted - " + reason + "] after [read#1] [artifact:x] [cite: c1 => read#2]"
	doc := Parse(source, Options{})
	if got := issueCodes(doc); !reflect.DeepEqual(got, []IssueCode{IssueMalformedArtifact}) {
		t.Fatalf("issues = %+v", doc.Issues)
	}
	if doc.Occurrences[0].Verify == nil || doc.Occurrences[0].Verify.Reason != reason {
		t.Fatalf("verify = %+v", doc.Occurrences[0])
	}
	if got := Project(doc); got != "Before. after" {
		t.Fatalf("presented %q", got)
	}
}

// AC-AST-17: a damaged token is suppressed and the next paragraph resumes.
func TestParseRecoversAtNewline(t *testing.T) {
	t.Parallel()
	doc := Parse("Lead [verify: targeted - never closed\n\nNext [read#1] paragraph.", Options{})
	want := []Issue{{Code: IssueUnterminatedNode, Kind: KindVerify, Span: Span{5, 37}, Token: "[verify: targeted - never closed"}}
	if !reflect.DeepEqual(doc.Issues, want) {
		t.Fatalf("issues = %+v", doc.Issues)
	}
	if len(doc.Occurrences) != 1 {
		t.Fatalf("no recovery: %+v", doc.Occurrences)
	}
	if got := Project(doc); got != "Lead\n\nNext paragraph." {
		t.Fatalf("presented %q", got)
	}
}

// AC-AST-18: an overlong node reports a structured issue and recovers at the next
// line, holding no more than the cap.
func TestParseRecoversFromOverflow(t *testing.T) {
	t.Parallel()
	source := "Lead [verify: targeted - " + strings.Repeat("x", MaxInlineProtocolNodeBytes) + " more\nNext [read#1] line."
	doc := Parse(source, Options{})
	if len(doc.Issues) != 1 || doc.Issues[0].Code != IssueNodeTooLong || doc.Issues[0].Span != (Span{5, strings.IndexByte(source, '\n')}) {
		t.Fatalf("issues = %+v", doc.Issues)
	}
	if len(doc.Issues[0].Token) > maxIssueTokenBytes {
		t.Fatalf("issue holds %d bytes", len(doc.Issues[0].Token))
	}
	if got := Project(doc); got != "Lead\nNext line." {
		t.Fatalf("presented %q", got)
	}
}

// AC-AST-19: node limits, malformed spans and recovery boundaries, counting bytes
// rather than characters.
func TestNodeLimits(t *testing.T) {
	t.Parallel()
	fill := func(prefix, closer string, total int, unit string) string {
		room := total - len(prefix) - len(closer)
		return prefix + strings.Repeat(unit, room/len(unit)) + strings.Repeat("x", room%len(unit)) + closer
	}
	exact := fill("[read#1 \"", "\"]", MaxInlineProtocolNodeBytes, "é")
	over := fill("[read#1 \"", "\"]", MaxInlineProtocolNodeBytes+1, "é")
	verifyExact := fill("[verify: targeted - ", "]", 2000+len("[verify: targeted - ]"), "é")
	longName := "[" + strings.Repeat("n", MaxInlineProtocolNodeBytes+10) + "] tail"
	longPath := "[" + strings.Repeat("a/", MaxInlineProtocolNodeBytes) + ":3] tail\nnext"
	cases := []struct {
		name, source string
		want         []IssueCode
		occurrences  int
		span         Span
	}{
		{"valid at the cap", exact + "\nafter", nil, 1, Span{}},
		{"valid maximal reason", verifyExact + "\nafter", nil, 1, Span{}},
		{"AC-AST-18 one byte over", over + "\nafter", []IssueCode{IssueNodeTooLong}, 0, Span{0, len(over)}},
		{"overlong at end of input", over, []IssueCode{IssueNodeTooLong}, 0, Span{0, len(over)}},
		{"AC-AST-17 newline terminated", "[read#1 \"abc\nafter", []IssueCode{IssueUnterminatedNode}, 0, Span{0, 12}},
		{"end of input malformed", "x [read#1 \"abc", []IssueCode{IssueUnterminatedNode}, 0, Span{2, 14}},
		{"overlong bare name stays prose", longName, nil, 0, Span{}},
		{"overlong bare path stays prose", longPath, nil, 0, Span{}},
		{"malformed then valid", "[read#x] [read#1]", []IssueCode{IssueMalformedCitation}, 1, Span{0, 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			if got := issueCodes(doc); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("issues = %+v", doc.Issues)
			}
			if len(doc.Occurrences) != tc.occurrences {
				t.Fatalf("occurrences = %d", len(doc.Occurrences))
			}
			if len(doc.Issues) > 0 && doc.Issues[0].Span != tc.span {
				t.Fatalf("span = %+v, want %+v", doc.Issues[0].Span, tc.span)
			}
		})
	}
}

// A released bracket rescans its contents, so a byte is offered a small constant
// number of times whatever the shape.
func TestScannerWorkIsBoundedBySize(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{"[", "[[", "[a ", "[a/", "[a,", "[\" ", "[\"[", "[\"a\" ", "[read#1 \"", "[read#1] ", "\\[", "]", "[verify: x\n", "a/"} {
		source := strings.Repeat(unit, (1<<16)/len(unit))
		doc := Document{Source: source}
		machine := newNodeMachine(source, Options{}, &documentBuilder{doc: &doc, budget: MaxOccurrences})
		offers := 0
		for machine.pos < len(source) {
			offers++
			machine.feed(source[machine.pos])
		}
		if offers > 4*len(source) {
			t.Errorf("unit %q took %d offers for %d bytes", unit, offers, len(source))
		}
	}
}

// AC-AST-20: a fence that is the entire response is the document, once.
func TestParseWholeResponseFence(t *testing.T) {
	t.Parallel()
	body := "At [read#3:4] and [a](source://main/x.go#L2).\n\n```\n[read#1]\n```\n"
	source := "\n````markdown\n" + body + "````\n\n"
	doc := Parse(source, Options{})
	if len(doc.Issues) != 0 || len(doc.Occurrences) != 2 {
		t.Fatalf("occurrences = %+v, issues = %+v", doc.Occurrences, doc.Issues)
	}
	if got := source[doc.Body.Start:doc.Body.End]; got != body {
		t.Fatalf("body = %q", got)
	}
	for _, o := range doc.Occurrences {
		if source[o.Span.Start:o.Span.End] != o.Token {
			t.Fatalf("span %v does not hold %q", o.Span, o.Token)
		}
	}
	if link := doc.Occurrences[1].Link; source[link.LabelSpan.Start:link.LabelSpan.End] != "a" {
		t.Fatalf("label span = %+v", link)
	}
	if len(doc.Zones) != 2 || source[doc.Zones[1].Span.Start:doc.Zones[1].Span.End] != "```\n[read#1]\n```\n" {
		t.Fatalf("zones = %+v", doc.Zones)
	}
	if got := Project(doc); strings.Contains(got, "markdown") || !strings.HasSuffix(got, "```\n[read#1]\n```\n") || !strings.HasPrefix(got, "At and [a](source://main/x.go#L2).") {
		t.Fatalf("presented %q", got)
	}
	ranges := TextRanges(source)
	if len(ranges) == 0 || ranges[0].Start != doc.Body.Start {
		t.Fatalf("text ranges = %+v", ranges)
	}

	unwrapped := []string{"```md\n[read#1]\n```", "~~~markdown\n[read#1]\n~~~", "```Markdown\n[read#1]\n```", "```md\n[read#1]\n"}
	for _, wrapped := range unwrapped {
		if doc := Parse(wrapped, Options{}); len(doc.Occurrences) != 1 {
			t.Errorf("%q: occurrences = %+v", wrapped, doc.Occurrences)
		}
	}
	literal := []string{
		"```python\n[read#1]\n```",
		"```\n[read#1]\n```",
		"```md title\n[read#1]\n```",
		"Intro\n\n```md\n[read#1]\n```",
		"```md\n[read#1]\n```\n\nOutro",
		"```md\n[read#1]\n```\n```md\n[read#2]\n```",
		"  ```md\n  [read#1]\n  ```",
		"- ```md\n  [read#1]\n  ```",
		"````md\n```md\n[read#1]\n```\n````",
		"```md\n```",
	}
	for _, source := range literal {
		if doc := Parse(source, Options{}); len(doc.Occurrences) != 0 {
			t.Errorf("%q: occurrences = %+v", source, doc.Occurrences)
		}
	}
}

// AC-AST-20: both workspace link forms carry coordinates from the destination.
func TestParseSourceLinks(t *testing.T) {
	t.Parallel()
	roots := Options{SourceRoot: func(id string) (string, bool) {
		switch id {
		case "main":
			return "", true
		case "lib":
			return "@shared/", true
		}
		return "", false
	}}
	knownSource := roots
	knownSource.KnownPath = func(name string) bool { return name == "a.go" }
	cases := []struct {
		name, source string
		opts         Options
		want         Citation
	}{
		{"relative", "[a](internal/a.go#L4)", roots, Citation{Path: "internal/a.go", Line: 4}},
		{"primary root", "[a](source://main/internal/a.go#L12-L20)", roots, Citation{Path: "internal/a.go", Line: 12, EndLine: 20}},
		{"secondary root", "[a](source://lib/internal/a.go#L12)", roots, Citation{Path: "@shared/internal/a.go", Line: 12}},
		{"unknown root is spelled to match nothing", "[a](source://nope/a.go#L1)", roots, Citation{Path: "source://nope/a.go", Line: 1}},
		{"one root", "[a](source://any/a.go#L1)", Options{}, Citation{Path: "a.go", Line: 1}},
		{"escaped", "[a](source://main/docs/a%20b.md#L1)", roots, Citation{Path: "docs/a b.md", Line: 1}},
		{"worker query", "[a](source://main/a.go?job_id=j#L3)", roots, Citation{Path: "a.go", Line: 3}},
		{"label never supplies coordinates", "[a.go:99](source://main/a.go)", knownSource, Citation{Path: "a.go"}},
		{"heading anchor", "[a](source://main/a.go#intro)", knownSource, Citation{Path: "a.go"}},
		{"upper case scheme", "[a](SOURCE://main/a.go#L4)", roots, Citation{Path: "a.go", Line: 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, tc.opts)
			if len(doc.Occurrences) != 1 || doc.Occurrences[0].Kind != KindWorkspaceLink {
				t.Fatalf("occurrences = %+v", doc.Occurrences)
			}
			if got := *doc.Occurrences[0].Citation; got != tc.want {
				t.Fatalf("citation = %+v", got)
			}
		})
	}
	for _, source := range []string{"[a](source:///a.go)", "[a](source://main)", "[a](source://u@main/a.go)", "[a](source:x)", "[a](<source://ma in/a.go>)"} {
		doc := Parse(source, roots)
		if got := issueCodes(doc); len(doc.Occurrences) != 0 || !reflect.DeepEqual(got, []IssueCode{IssueMalformedCitation}) {
			t.Errorf("%q: occurrences = %+v, issues = %v", source, doc.Occurrences, got)
		}
	}
	if doc := Parse("[a](source://main/a.go)", roots); len(doc.Occurrences) != 0 || len(doc.Issues) != 0 {
		t.Errorf("an unobserved source link with no coordinate is ordinary: %+v", doc)
	}
}

// The presentation of the dialect's constructs, byte for byte.
func TestProjectFixtures(t *testing.T) {
	t.Parallel()
	opts := knownNames("LICENSE")
	cases := []struct{ name, source, want string }{
		{"empty", "", ""},
		{"only whitespace", "   \n\n", ""},
		{"trailing newlines", "x\n\n", "x"},
		{"note", "The timeout is in [read#3:42]. I dispatched a worker [task#1].", "The timeout is in. I dispatched a worker."},
		{"directive line", "Fixed [read#3:42-48].\n[verify: targeted - Checked it] [command#4]", "Fixed."},
		{"paths", "Intro.\n\n[verify: project - full]\n\nOutro [internal/a.go:42] and [\"docs/a b.md\":3].\n", "Intro.\n\nOutro [internal/a.go:42](internal/a.go#L42) and [docs/a b.md:3](<docs/a b.md#L3>)."},
		{"leading directive", "[verify: blocked - no access]\n\nBody.", "Body."},
		{"group", "a [internal/a.go:1, read#2] b [read#1, read#2] c", "a [internal/a.go:1](internal/a.go#L1) b c"},
		{"known and unknown", "[LICENSE] and [sic]", "[LICENSE](LICENSE) and [sic]"},
		{"heading", "# Heading [read#1]\n\nText\n===\n", "# Heading\n\nText\n==="},
		{"fence", "```\n[read#1]\n\n\nx\n```\nafter [read#1]", "```\n[read#1]\n\n\nx\n```\nafter"},
		{"fence in list", "- item [read#1]\n\n  ```\n  [read#2]\n  ```\n\n  after [read#3]\n- next\n", "- item\n\n  ```\n  [read#2]\n  ```\n\n  after\n- next"},
		{"fence in quote", "> a [read#1]\n> ```\n> [read#2]\n> ```\n\nout [read#4]", "> a\n> ```\n> [read#2]\n> ```\n\nout"},
		{"fence ended by its container", "> ```\n> [read#1]\nout [read#2]", "> ```\n> [read#1]\nout"},
		{"indented", "para\n\n    [read#1]\n\n    [read#2]\n\nafter [read#3]", "para\n\n    [read#1]\n\n    [read#2]\n\nafter"},
		{"lazy paragraph is not code", "para\n    [read#1]", "para"},
		{"comment block", "<!--\n[read#1]\n\n[read#2]\n-->\n[read#3]", "<!--\n[read#1]\n\n[read#2]\n-->\n"},
		{"html block", "<div>\n[read#1]\n\n[read#2]", "<div>\n[read#1]\n"},
		{"unclosed comment is prose", "a <!-- [read#1] unclosed\n\n[read#2]", "a <!-- unclosed"},
		{"not a tag", "a <b and [read#1] c > d", "a <b and c > d"},
		{"code spans", "`[read#1]` and [read#2] and ``x ` [read#3]`` end", "`[read#1]` and and ``x ` [read#3]`` end"},
		{"unclosed tick", "`unclosed [read#1] tail", "`unclosed tail"},
		{"multi-line code span", "a `b\nc [read#1] d` e [read#2]", "a `b\nc [read#1] d` e"},
		{"escapes", `\[read#1] and \\[read#2]`, `\[read#1] and \\`},
		{"link kept", "[auth.go](internal/auth.go#L42) [read#1]", "[auth.go](internal/auth.go#L42)"},
		{"invalid destination", "[read#1](bad dest) tail", "(bad dest) tail"},
		{"nested label", "[see [read#3] now](https://example.test/f) [read#2]", "[see [read#3] now](https://example.test/f)"},
		{"reference", "[r]: https://example.test/r\n\n[read#1][r] [read#2][none]", "[r]: https://example.test/r\n\n[read#1][r] [none]"},
		{"late definition", "Use [read#1] here.\n\n[read#1]: https://example.test/a\n", "Use [read#1] here.\n\n[read#1]: https://example.test/a"},
		{"table", "| a | b |\n|---|---|\n| [read#1] | `[read#2]` |\n", "| a | b |\n|---|---|\n| | `[read#2]` |"},
		{"spacing", "a  [read#1]  b\n([read#1]) x\n  [read#1]  \n\n\n\n  text", "a  b\n() x\ntext"},
		{"blank line after a dropped line", "a\n\n[read#1]\n\n[read#2]\n\nb", "a\n\nb"},
		{"literal after a removal on its line", "[read#1] `x`\ny", "`x`\ny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Project(Parse(tc.source, opts)); got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}
