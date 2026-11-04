package citationmd

import (
	"reflect"
	"strings"
	"testing"
)

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
	if len(projector.Issues()) != 1 || projector.Issues()[0].Code != IssueMalformedArtifact {
		t.Fatalf("issues = %+v", projector.Issues())
	}
}

// AC-AST-16: ordinary prose is released as soon as it is disambiguated.
func TestProjectorReleasesProsePromptly(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, source, want string }{
		{"AC-AST-16 leg prefix", "[leg-of-lamb is", "[leg-of-lamb is"},
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
