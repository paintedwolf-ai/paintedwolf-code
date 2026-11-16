package reproducedsource_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/reproducedsource"
)

func parseDoc(src string) citationmd.Document {
	return citationmd.Parse(src, citationmd.Options{})
}

func randomProseChunk(rng *rand.Rand) string {
	words := []string{
		"router", "handler", "session", "dispatch", "context", " ledger",
		"verify", "payload", "adapter", "transport", "observer", "anchor",
	}
	var b strings.Builder
	paragraphs := 1 + rng.IntN(4)
	for p := range paragraphs {
		if p > 0 {
			b.WriteString("\n\n")
		}
		switch rng.IntN(5) {
		case 0:
			b.WriteString(strings.Repeat("#", 1+rng.IntN(3)))
			b.WriteByte(' ')
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteString(" summary")
		case 1:
			for item := range 1 + rng.IntN(4) {
				if item > 0 {
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "- item %s with `return validateRequest(ctx, req)` inline", words[rng.IntN(len(words))])
			}
		case 2:
			fmt.Fprintf(&b, "> note on [%s](internal/server/handler.go#L4) and `close(ch)`", words[rng.IntN(len(words))])
		case 3:
			for n := range 1 + rng.IntN(3) {
				if n > 0 {
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "%d. step %s checks invariants", n+1, words[rng.IntN(len(words))])
			}
		default:
			for w := range 4 + rng.IntN(12) {
				if w > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(words[rng.IntN(len(words))])
			}
			b.WriteByte('.')
		}
	}
	return b.String()
}

// Reproduced-source facts depend only on fence contents and candidate file text.
func TestMeasureProseInvarianceProperty(t *testing.T) {
	candidates := []reproducedsource.Candidate{
		{
			DisplayPath: "internal/server/handler.go",
			Content: strings.Join([]string{
				"package server",
				"",
				"func Handle(ctx Context, req Request) error {",
				"\tif err := validateRequest(ctx, req); err != nil {",
				"\t\treturn err",
				"\t}",
				"\treturn dispatchRequest(ctx, req)",
				"}",
			}, "\n"),
		},
		{
			DisplayPath: "internal/server/config.go",
			Content: strings.Join([]string{
				"package server",
				"type Config struct {",
				"\tAddr string",
				"\tPort int",
				"}",
			}, "\n"),
		},
	}

	fenceA := "```go\nfunc Handle(ctx Context, req Request) error {\n\tif err := validateRequest(ctx, req); err != nil {\n\t\treturn err\n\t}\n\treturn dispatchRequest(ctx, req)\n}\n```"
	fenceB := "```go\ntype Config struct {\n\tAddr string\n}\n```"

	baselineDoc := parseDoc(fenceA + "\n\n" + fenceB + "\n")
	want := reproducedsource.Measure(baselineDoc, nil, candidates)
	if want.Lines != 6 || want.Path != "internal/server/handler.go" || want.StartLine != 3 || want.EndLine != 8 || want.FileLines != 7 {
		t.Fatalf("baseline = %+v", want)
	}

	rng := rand.New(rand.NewPCG(0x5253_3031, 0x4931))
	for trial := range 150 {
		before := randomProseChunk(rng)
		middle := randomProseChunk(rng)
		after := randomProseChunk(rng)
		markdown := before + "\n\n" + fenceA + "\n\n" + middle + "\n\n" + fenceB + "\n\n" + after + "\n"
		got := reproducedsource.Measure(parseDoc(markdown), nil, candidates)
		if got != want {
			t.Fatalf("trial %d: Measure() = %+v, want %+v\nmarkdown:\n%s", trial, got, want, markdown)
		}
	}
}

// Generated code absent from candidates yields zero values even when sharing short delimiter lines.
func TestMeasureAbsentCodeYieldsZeroProperty(t *testing.T) {
	candidates := []reproducedsource.Candidate{
		{
			DisplayPath: "pkg/alpha/alpha.go",
			Content: strings.Join([]string{
				"package alpha",
				"",
				"func ExistingAlpha(input string) string {",
				"\tif input == \"\" {",
				"\t\treturn \"default\"",
				"\t}",
				"\treturn input",
				"}",
			}, "\n"),
		},
		{
			DisplayPath: "pkg/beta/beta.ts",
			Content: strings.Join([]string{
				"export function computeBeta(items: string[]): number {",
				"  return items.filter(Boolean).length;",
				"}",
			}, "\n"),
		},
	}

	langs := []string{"go", "ts", "python", "sh", ""}
	shortLines := []string{"{", "}", ");", "});", "end"}
	rng := rand.New(rand.NewPCG(0x5253_3031, 0x4932))

	for trial := range 150 {
		lineCount := 8 + rng.IntN(205)
		var body []string
		for lineIdx := range lineCount {
			if rng.IntN(4) == 0 {
				body = append(body, shortLines[rng.IntN(len(shortLines))])
				continue
			}
			body = append(body, fmt.Sprintf("  synthesizedStep_%d_%d := NovelTransform(%d)", trial, lineIdx, rng.IntN(100000)))
		}
		lang := langs[rng.IntN(len(langs))]
		var md string
		if trial%7 == 0 {
			indented := make([]string, len(body))
			for i, l := range body {
				indented[i] = "    " + l
			}
			md = "Proposed snippet:\n\n" + strings.Join(indented, "\n") + "\n"
		} else {
			md = fmt.Sprintf("Here is the new script:\n\n```%s\n%s\n```\n", lang, strings.Join(body, "\n"))
		}
		got := reproducedsource.Measure(parseDoc(md), nil, candidates)
		if got != (reproducedsource.Result{}) {
			t.Fatalf("trial %d: Measure() = %+v, want zero Result", trial, got)
		}
	}
}
