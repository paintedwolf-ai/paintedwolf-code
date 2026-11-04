package citationmd

import (
	"reflect"
	"strings"
	"testing"
)

// streamCorpus holds the constructs the structural parse and the block tracker
// must agree on.
var streamCorpus = []string{
	"",
	"plain text",
	"The timeout originates in [read#3:42]. I'm checking its callers.\nI dispatched the second worker [task#1].",
	"The timeout uses the deadline [read#3:42-48].\n[verify: targeted - Checked the authentication package] [command#4]",
	"Intro.\n\n[verify: project - full]\n\nOutro [internal/a.go:42] and [\"docs/a b.md\":3].\n",
	"[verify: blocked - no access]\n\nBody.",
	"[read#1]",
	"a [read#1, read#2] b",
	"a [internal/a.go:1, read#2] b [c/d.go, e/f.go] g",
	"[LICENSE] and [sic]",
	"[artifact: 11111111-1111-1111-1111-111111111111] tail",
	"[cite: c1 => read#5:42]\n[cite: c2 => <https://example.test/a>]",
	"# Heading [read#1]\n\nText\n===\n\nMore\n---\n***\n",
	"```\n[read#1]\n```\n",
	"```go\n[read#1]\n\n\nx\n```\nafter [read#1]",
	"~~~\n[read#1]\n~~~~\n[read#2]",
	"````\n```\n[read#1]\n```\n````\n[read#2]",
	"```\nunclosed [read#1]\n\n",
	"``` a`b\n[read#1]",
	"- item [read#1]\n\n  ```\n  [read#2]\n  ```\n\n  after [read#3]\n- next [read#4]\n",
	"1. one\n\n   ```\n   [read#1]\n   ```\n2. two [read#2]\n",
	"> quote [read#1]\n> ```\n> [read#2]\n> ```\n> after [read#3]\n\nout [read#4]",
	"> ```\n> [read#1]\nout [read#2]",
	"- > ```\n  > [read#1]\n  > ```\n  > [read#2]\n",
	"- a\n\n      [read#1]\n\n  [read#2]\n",
	"para\n\n    [read#1]\n\n    [read#2]\n\nafter [read#3]",
	"para\n    [read#1]",
	"\tcode [read#1]\n",
	"- a\n\t[read#1]\n",
	"> lazy\ncontinuation [read#1]\n\n    code [read#2]",
	"<!-- [read#1] -->\n[read#2]",
	"<!--\n[read#1]\n\n[read#2]\n-->\n[read#3]",
	"<div>\n[read#1]\n\n[read#2]",
	"<pre>\n[read#1]\n\n[read#2]\n</pre>\n[read#3]",
	"<custom-tag>\n[read#1]\n\n[read#2]",
	"para\n<custom-tag>\n[read#1]",
	"<?php [read#1] ?>\n[read#2]",
	"<!DOCTYPE [read#1]>\n[read#2]",
	"<![CDATA[ [read#1] ]]>\n[read#2]",
	"a <!-- [read#1] --> b [read#2]",
	"a <!-- [read#1] unclosed\n\n[read#2]",
	`a <span title="[read#1]">x</span> [read#2]`,
	"a <https://example.test/[read#1]> [read#2]",
	"a <b and [read#1] c > d",
	"a < b [read#1]",
	"`[read#1]` and [read#2] and ``x ` [read#3]`` end",
	"`unclosed [read#1] tail\n\n[read#2]",
	"a `b\nc [read#1] d` e [read#2]",
	"``` x [read#1]",
	"\\`[read#1]\\`",
	`\[read#1] and \\[read#2]`,
	`[read#1\] tail`,
	"[auth.go](internal/auth.go#L42) and [read#3](https://example.test/a) [read#1]",
	"[read#1](bad dest) tail",
	"[read#1](<ok dest> \"title\") tail [read#2]",
	"[read#1]( x 'title' ) [read#2]",
	"[read#1](a(b)c) [read#2]",
	"[a](b[read#1]c) [read#2]",
	"[see [read#3] now](https://example.test/f) [read#2]",
	"[see [read#3] not a link] [read#2]",
	"[r]: https://example.test/r\n\n[read#1][r] and [read#2][none] and [r] tail",
	"[read#1]: https://example.test/r\n\n[read#1] and [read#1][] and [READ#1] end [read#2]",
	"[read#1]: https://example.test/a \"t\"\n[read#2]: https://example.test/b\n\n[read#2]",
	"[foo]: https://example.test/a\n[read#2]: https://example.test/b\n\n[read#2]",
	"text [read#1]: not a definition",
	"[read#1]: two words",
	"see https://example.test/a[read#1] now [read#2]",
	"see www.example.test/a[read#1] now",
	"see http://localhost/a [read#1]",
	"- [x] done [read#1]\n- [ ] open\n",
	"| a | b |\n|---|---|\n| [read#1] | `[read#2]` |\n",
	"[and/or maybe] then [a/b.go]",
	"[leg-of-lamb is tasty] and [leg-a:read#1]",
	"An [open bracket\nthen [read#1].",
	"[verify: targeted - x\n\nNext paragraph [read#1].",
	"[read#3:42 \"unterminated\n\nNext.",
	"[read#3",
	"[verify: targeted",
	"[read#3 ",
	"tail [",
	"tail [a/b",
	"[read#3:42 \"a, b; \\\"c\\\" ]\"] tail",
	"[read#1:2 \"a `b` c\"] tail `code`",
	"a  [read#1]  b",
	"a\t[read#1]\tb",
	"([read#1]) and [read#1].",
	"  [read#1]  \n\n\n\n  text",
	"日本語 [docs/設計.md:7] 日本語 [read#1]",
	"   \n\n",
	"x\n\n",
	"- [read#1]\n> [read#2]\n",
	"a\n[read#1]\n[read#2]\nb",
	"a\n\n[read#1]\n\n[read#2]\n\nb",
	"* * *\n+ plus\n\n12. twelve\n13) thirteen\n\npara\n2. not a list\n\n1234567890. no\n",
	"=\n\na\n=\n#nospace [read#1]\n####### seven\n",
	"-\n\npara\n-\n\n*\n\npara\n*\n\n__\n\n___\n\n-x\n",
	"1.\n\n123",
	"     \n> \n>\n> a\n>\n\n",
	"\t\t[read#1]\n",
	"<",
	"<a",
	"<!",
	"<!-",
	"<!x [read#1]",
	"a <! [read#1]",
	"a <!X [read#1] > b",
	"a <? [read#1] ?> b",
	"a <![CDATA[ [read#1] ]]> b",
	"a </b> [read#1]",
	"a <x@example.test> [read#1]",
	"]( [read#1]",
	"[a](b\n[read#1]",
	"[read#1](\n[read#2]",
	"[read#1][\n[read#2]",
	"[read#1]: \n[read#2]",
	"[a b](c)",
	"[a b](<c",
	"[a b](<c\\>d>)",
	"[a b](c \"d\\\"e\")",
	"[a b](c (d))",
	"[a b](c x)",
	"[a b](\\))",
	"[a b](c \"t\" x)",
	"[a [b] c][d]",
}

func splitEvery(t *testing.T, source string, opts Options) {
	t.Helper()
	whole := NewProjector(opts)
	want := whole.Write(source) + whole.Close()
	for cut := 1; cut < len(source); cut++ {
		split := NewProjector(opts)
		got := split.Write(source[:cut])
		got += split.Write(source[cut:])
		got += split.Close()
		if got != want {
			t.Fatalf("split at %d of %q:\n got %q\nwant %q", cut, source, got, want)
		}
		if !reflect.DeepEqual(split.Issues(), whole.Issues()) || split.Status() != whole.Status() {
			t.Fatalf("split at %d of %q changed outcomes: %+v / %+v", cut, source, split.Issues(), whole.Issues())
		}
	}
	bytewise := NewProjector(opts)
	var got strings.Builder
	for i := 0; i < len(source); i++ {
		got.WriteString(bytewise.Write(source[i : i+1]))
	}
	got.WriteString(bytewise.Close())
	if got.String() != want {
		t.Fatalf("bytewise %q:\n got %q\nwant %q", source, got.String(), want)
	}
}

// AC-AST-15: every split gives the one-shot result, which agrees with the final
// structural presentation.
func TestProjectorAgreesAtEveryBoundary(t *testing.T) {
	t.Parallel()
	opts := knownNames("LICENSE")
	for _, source := range streamCorpus {
		splitEvery(t, source, opts)
		projector := NewProjector(opts)
		got := projector.Write(source) + projector.Close()
		doc := Parse(source, opts)
		if want := Project(doc); got != want {
			t.Errorf("stream differs from structural presentation for %q:\n stream %q\n parsed %q", source, got, want)
		}
		if !reflect.DeepEqual(projector.Issues(), nodeIssues(doc)) {
			t.Errorf("issues differ for %q:\n stream %+v\n parsed %+v", source, projector.Issues(), nodeIssues(doc))
		}
	}
}

// nodeIssues drops the document-level issues a stream has no view of.
func nodeIssues(doc Document) []Issue {
	var out []Issue
	for _, issue := range doc.Issues {
		if issue.Code != IssueConflictingVerification && issue.Code != IssueOccurrenceBudget && issue.Code != IssueSourceBudget {
			out = append(out, issue)
		}
	}
	return out
}

// AC-AST-15: a valid maximal assessment and a late tool call leave no raw
// directive in any prefix.
func TestProjectorNeverLeaksDirectives(t *testing.T) {
	t.Parallel()
	reason := strings.Repeat("reason ", 285) + "ends"
	source := "Before. [verify: targeted - " + reason + "] after [read#1] [artifact:x] [cite: c1 => read#2]"
	splitEvery(t, source, Options{})
	projector := NewProjector(Options{})
	var visible strings.Builder
	for i := 0; i < len(source); i++ {
		visible.WriteString(projector.Write(source[i : i+1]))
		for _, leak := range []string{"[verify", "verify:", "reason", "read#", "artifact:", "cite:"} {
			if strings.Contains(visible.String(), leak) {
				t.Fatalf("prefix %d leaked %q: %q", i, leak, visible.String())
			}
		}
	}
	visible.WriteString(projector.Close())
	if got := visible.String(); got != "Before. after" {
		t.Fatalf("visible = %q", got)
	}
	if len(projector.Issues()) != 2 {
		t.Fatalf("issues = %+v", projector.Issues())
	}
}

// AC-AST-16: ordinary prose is released as soon as it is disambiguated.
func TestProjectorReleasesProsePromptly(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, source, want string }{
		{"AC-AST-16 leg prefix", "[leg-of-lamb is", "[leg-of-lamb"},
		{"AC-AST-16 unclosed bracket", "[see the", "[see the"},
		{"non path byte", "[a!b", "[a!b"},
		{"unknown name at close", "[sic] x", "[sic] x"},
		{"multi-segment prose", "[and/or maybe", "[and/or maybe"},
		{"uppercase kind", "[Read#3 and", "[Read#3 and"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projector := NewProjector(Options{})
			if got := projector.Write(tc.source); got != tc.want {
				t.Fatalf("visible = %q, want %q", got, tc.want)
			}
			if projector.Status().Provisional {
				t.Fatal("prose set the provisional status")
			}
		})
	}
}

// AC-AST-17: a damaged token is suppressed and the next paragraph resumes.
func TestProjectorRecoversAtNewline(t *testing.T) {
	t.Parallel()
	source := "Lead [verify: targeted - never closed\n\nNext paragraph."
	projector := NewProjector(Options{})
	got := projector.Write(source) + projector.Close()
	if got != "Lead\n\nNext paragraph." {
		t.Fatalf("visible = %q", got)
	}
	if !projector.Status().Provisional {
		t.Fatal("no provisional status")
	}
	want := []Issue{{Code: IssueUnterminatedNode, Kind: KindVerify, Span: Span{5, 37}, Token: "[verify: targeted - never closed"}}
	if !reflect.DeepEqual(projector.Issues(), want) {
		t.Fatalf("issues = %+v", projector.Issues())
	}
	if doc := Parse(source, Options{}); !reflect.DeepEqual(doc.Issues, want) {
		t.Fatalf("parser issues = %+v", doc.Issues)
	}
}

// AC-AST-18: an overlong node reports at once, holds nothing, and recovers at
// the next line.
func TestProjectorRecoversFromOverflow(t *testing.T) {
	t.Parallel()
	body := "[verify: targeted - " + strings.Repeat("x", MaxInlineProtocolNodeBytes)
	source := "Lead " + body + " more\nNext line."
	projector := NewProjector(Options{})
	cut := len("Lead ") + MaxInlineProtocolNodeBytes
	visible := projector.Write(source[:cut])
	if projector.Status().Provisional {
		t.Fatal("provisional before the node passed the cap")
	}
	visible += projector.Write(source[cut : cut+1])
	if !projector.Status().Provisional {
		t.Fatal("no provisional status once the node passed the cap")
	}
	if len(projector.machine.scanner.buf) > MaxInlineProtocolNodeBytes {
		t.Fatalf("held %d bytes", len(projector.machine.scanner.buf))
	}
	visible += projector.Write(source[cut+1:]) + projector.Close()
	if visible != "Lead\nNext line." {
		t.Fatalf("visible = %q", visible)
	}
	issues := projector.Issues()
	if len(issues) != 1 || issues[0].Code != IssueNodeTooLong || issues[0].Span != (Span{5, strings.IndexByte(source, '\n')}) {
		t.Fatalf("issues = %+v", issues)
	}
	if doc := Parse(source, Options{}); !reflect.DeepEqual(doc.Issues, issues) {
		t.Fatalf("parser issues = %+v, stream issues = %+v", doc.Issues, issues)
	}
}

// AC-AST-19: the parser and the projector share node limits, malformed spans and
// recovery boundaries, counting bytes rather than characters.
func TestNodeLimitsAgree(t *testing.T) {
	t.Parallel()
	fill := func(prefix, closer string, total int, unit string) string {
		room := total - len(prefix) - len(closer)
		return prefix + strings.Repeat(unit, room/len(unit)) + strings.Repeat("x", room%len(unit)) + closer
	}
	exact := fill("[read#1 \"", "\"]", MaxInlineProtocolNodeBytes, "é")
	over := fill("[read#1 \"", "\"]", MaxInlineProtocolNodeBytes+1, "é")
	verifyExact := fill("[verify: targeted - ", "]", 2000+len("[verify: targeted - ]"), "é")
	longName := "[" + strings.Repeat("n", MaxInlineProtocolNodeBytes+10) + "] tail"
	longPath := "[" + strings.Repeat("a/", MaxInlineProtocolNodeBytes) + " tail\nnext"
	cases := []struct {
		name, source string
		wantCodes    []IssueCode
		occurrences  int
	}{
		{"valid at the cap", exact + "\nafter", nil, 1},
		{"valid maximal reason", verifyExact + "\nafter", nil, 1},
		{"AC-AST-18 one byte over", over + "\nafter", []IssueCode{IssueNodeTooLong}, 0},
		{"overlong at end of input", over, []IssueCode{IssueNodeTooLong}, 0},
		{"AC-AST-17 newline terminated", "[read#1 \"abc\nafter", []IssueCode{IssueUnterminatedNode}, 0},
		{"end of input malformed", "x [read#1 \"abc", []IssueCode{IssueUnterminatedNode}, 0},
		{"overlong single segment stays prose", longName, nil, 0},
		{"overlong path is a damaged node", longPath, []IssueCode{IssueNodeTooLong}, 0},
		{"malformed then valid", "[read#x] [read#1]", []IssueCode{IssueMalformedCitation}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			if got := issueCodes(doc); !reflect.DeepEqual(got, tc.wantCodes) {
				t.Fatalf("issues = %+v", doc.Issues)
			}
			if len(doc.Occurrences) != tc.occurrences {
				t.Fatalf("occurrences = %d", len(doc.Occurrences))
			}
			projector := NewProjector(Options{})
			got := projector.Write(tc.source) + projector.Close()
			if !reflect.DeepEqual(projector.Issues(), doc.Issues) {
				t.Fatalf("stream issues = %+v, parser issues = %+v", projector.Issues(), doc.Issues)
			}
			if want := Project(doc); got != want {
				t.Fatalf("stream %q, parsed %q", got, want)
			}
		})
	}
}

// A later byte can change an earlier structure in exactly one way the stream
// cannot foresee: a definition after its use. The stream errs by hiding the
// token, never by showing a protocol token as if it were accepted text.
func TestProjectorLateDefinitionHidesRatherThanLeaks(t *testing.T) {
	t.Parallel()
	source := "Use [read#1] here.\n\n[read#1]: https://example.test/a\n"
	projector := NewProjector(Options{})
	got := projector.Write(source) + projector.Close()
	if !strings.HasPrefix(got, "Use here.") {
		t.Fatalf("stream = %q", got)
	}
	if final := Project(Parse(source, Options{})); !strings.HasPrefix(final, "Use [read#1] here.") {
		t.Fatalf("final = %q", final)
	}
}

// Replay stays linear: work is counted in bytes offered, not time.
func TestProjectorWorkIsBoundedBySize(t *testing.T) {
	t.Parallel()
	const size = 1 << 16
	adversarial := []string{
		"`[",
		"``[ ",
		"<a ",
		"<a \"",
		"<!-- [",
		"[a b [",
		"[a](",
		"[read#1](",
		"[read#1][",
		"[",
		"[[",
		"]",
		"> ",
		"- ",
		" ",
		"\n",
		"`\n",
		"[read#1 \"",
		"a/",
	}
	for _, unit := range adversarial {
		source := strings.Repeat(unit, size/len(unit))
		projector := NewProjector(Options{})
		projector.Write(source)
		projector.Close()
		if limit := 8 * len(source); projector.work > limit {
			t.Errorf("unit %q took %d steps for %d bytes", unit, projector.work, len(source))
		}
		Parse(source, Options{})
	}
}

func TestFileLinkForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path      string
		line, end int
		want      string
	}{
		{"internal/a.go", 0, 0, "[internal/a.go](internal/a.go)"},
		{"internal/a.go", 42, 0, "[internal/a.go:42](internal/a.go#L42)"},
		{"internal/a.go", 42, 58, "[internal/a.go:42-58](internal/a.go#L42-L58)"},
		{"@shared/lib/sec.go", 12, 0, "[@shared/lib/sec.go:12](@shared/lib/sec.go#L12)"},
		{"docs/a b.md", 3, 0, "[docs/a b.md:3](<docs/a b.md#L3>)"},
		{"docs/a_b*c.md", 0, 0, `[docs/a\_b\*c.md](docs/a_b*c.md)`},
		{"docs/c#.md", 1, 0, "[docs/c#.md:1](docs/c%23.md#L1)"},
		{"a:b/c.go", 0, 0, "[a:b/c.go](./a:b/c.go)"},
		{"docs/設計.md", 7, 0, "[docs/設計.md:7](docs/設計.md#L7)"},
	}
	for _, tc := range cases {
		link := FileLink(tc.path, tc.line, tc.end)
		if link != tc.want {
			t.Errorf("FileLink(%q) = %q, want %q", tc.path, link, tc.want)
		}
		doc := Parse(link, Options{})
		if len(doc.Occurrences) != 1 || doc.Occurrences[0].Kind != KindWorkspaceLink {
			t.Errorf("%q does not parse as a workspace link: %+v", link, doc.Occurrences)
			continue
		}
		if c := doc.Occurrences[0].Citation; c.Line != tc.line || c.EndLine != tc.end || strings.TrimPrefix(c.Path, "./") != tc.path {
			t.Errorf("%q round trip = %+v", link, c)
		}
	}
}

func TestPlainLabel(t *testing.T) {
	t.Parallel()
	cases := []struct{ label, want string }{
		{"RFC 9110", "RFC 9110"},
		{" ", ""},
		{"https://example.test/a", "`https://example.test/a`"},
		{"www.example.test", "`www.example.test`"},
	}
	for _, tc := range cases {
		if got := PlainLabel(Link{Label: tc.label}); got != tc.want {
			t.Errorf("PlainLabel(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}

func TestRenderDecisions(t *testing.T) {
	t.Parallel()
	source := "A [read#1, a/b.go:2] B [L](https://example.test/x) C [read#2] D [M](https://example.test/y) E [N](https://example.test/z).\n"
	got := Render(Parse(source, Options{}), func(o Occurrence) Rendition {
		switch {
		case o.Citation != nil && o.Citation.Handle == "read#1":
			return Rendition{Text: "one"}
		case o.Citation != nil && o.Citation.Path != "":
			return Rendition{Text: "two"}
		case o.Link != nil && o.Link.Label == "L":
			return Rendition{Keep: true}
		case o.Link != nil && o.Link.Label == "M":
			return Rendition{Text: PlainLabel(*o.Link)}
		default:
			return Rendition{}
		}
	})
	if want := "A one, two B [L](https://example.test/x) C D M E."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
