package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseCoordinatorCompletionReport(t *testing.T) {
	t.Parallel()
	envelope := `{"synthesis":"Report body.","cited_evidence":[{"path":"a.go","line":1,"excerpt":"x"}]}`
	if _, ok := ParseCoordinatorCompletionReport(envelope); !ok {
		t.Fatal("expected bare json envelope")
	}
	if _, ok := ParseCoordinatorCompletionReport("Intro\n" + envelope); ok {
		t.Fatal("expected hybrid prose to fail parse")
	}
	if _, ok := ParseCoordinatorCompletionReport("```json\n" + envelope + "\n```"); !ok {
		t.Fatal("expected fenced-only envelope")
	}
}

// A fence alone repairs the pinned body: it needs that body, and never
// replaces it with new prose.
func TestReadCloseoutReportJoinsARepairFenceToItsPinnedBody(t *testing.T) {
	t.Parallel()
	const pinned = "## Report\n\nA retained body."
	const trailer = "```json\n{\"cited_evidence\":[{\"path\":\"list#1\"}],\"cited_urls\":[],\"artifact_ids\":[]}\n```"
	read, ok := ReadCloseoutReport(trailer, pinned)
	if !ok || read.Report.Synthesis != pinned || len(read.Report.CitedEvidence) != 1 || read.Report.CitedEvidence[0].Path != "list#1" {
		t.Fatalf("read = %+v ok=%v", read, ok)
	}
	if _, ok := ReadCloseoutReport(trailer, ""); ok {
		t.Fatal("a fence alone must require a pinned body")
	}
	read, ok = ReadCloseoutReport("```json\n{\"cited_evidence\":[],\"extra\":true}\n```", pinned)
	if !ok || len(read.Unread) != 1 || read.Unread[0].Path != "extra" {
		t.Fatalf("unknown member not named: %+v ok=%v", read, ok)
	}
}

// Every member the report does not take is named with where it sits, and the
// refusal groups repeats: a field placed inside each finding is one entry.
func TestReadCloseoutReportNamesUnreadMembers(t *testing.T) {
	t.Parallel()
	content := "## Report\n\nBody.\n\n```json\n" + `{
		"synthesis": "Body again.",
		"findings": [
			{"id": "c1", "title": "A", "disposition": "act", "ask": {"do": "x", "effort": "small"}, "statement": "s"},
			{"id": "c2", "title": "B", "disposition": "held", "ask": {"do": "y", "effort": "small"}, "answers": "none"}
		],
		"set_asides": [{"scanner": "s", "paths": ["t/**"], "reason": "fixtures"}]
	}` + "\n```"
	read, ok := ReadCloseoutReport(content, "")
	if !ok || read.Report.Synthesis != "## Report\n\nBody." || len(read.Report.Findings) != 2 || len(read.Report.SetAsides) != 1 {
		t.Fatalf("read = %+v ok=%v, want the answer above the fence and every readable field", read.Report, ok)
	}
	sample, total := UnreadReportFields(read.Unread)
	want := []string{
		"`findings[].ask` (2): `ask` is a top-level report field",
		"`findings[].statement`: not a report field",
		"`findings[].answers`: want object",
		"`synthesis`: the answer is the Markdown above the fence",
	}
	if total != len(want) || strings.Join(sample, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unread fields = %q (total %d), want %q", sample, total, want)
	}
	issue, ok := ReportFenceUnreadable(read.Unread)
	if !ok || issue.Code != ReportFenceUnreadableCode || issue.Count != len(want) {
		t.Fatalf("refusal = %+v", issue)
	}
}

// A finding that names its id but lost its title stays in the read, so the
// document check can ask for the title; one with neither is empty.
func TestReadCloseoutReportKeepsAnIdentifiedUntitledFinding(t *testing.T) {
	t.Parallel()
	content := "## Report\n\nBody.\n\n```json\n" +
		`{"findings": [{"id": "c1", "disposition": "held"}, {"disposition": "act"}, {"id": "c2", "title": "B", "disposition": "act"}]}` +
		"\n```"
	read, ok := ReadCloseoutReport(content, "")
	if !ok || len(read.Report.Findings) != 2 || read.Report.Findings[0].ID != "c1" || read.Report.Findings[1].ID != "c2" {
		t.Fatalf("findings = %+v ok=%v, want the untitled c1 kept beside c2", read.Report.Findings, ok)
	}
}

func TestCoordinatorCloseoutTranscriptNarrative(t *testing.T) {
	t.Parallel()

	const fullReport = "## Code-quality survey\n\n### SSOT drift\n\nDuplicate constants across models."
	const stub = "One-paragraph executive summary."
	const envelope = `{"synthesis":"One-paragraph executive summary.","cited_evidence":[{"path":"a.go","line":1,"excerpt":"x"}]}`

	t.Run("json only projects synthesis", func(t *testing.T) {
		t.Parallel()
		got, ok := CoordinatorCloseoutTranscriptNarrative(envelope)
		if !ok || got != stub {
			t.Fatalf("narrative = %q, ok = %v", got, ok)
		}
	})

	t.Run("full report in synthesis projects verbatim", func(t *testing.T) {
		t.Parallel()
		content := `{"synthesis":"## Code-quality survey\n\n### SSOT drift\n\nDuplicate constants across models.","cited_evidence":[{"path":"a.go","line":1,"excerpt":"x"}]}`
		got, ok := CoordinatorCloseoutTranscriptNarrative(content)
		if !ok || got != fullReport {
			t.Fatalf("narrative = %q, want full synthesis body", got)
		}
	})

	t.Run("hybrid markdown plus fence rejected", func(t *testing.T) {
		t.Parallel()
		leading := "## Code-quality survey\n\nDuplicate constants across models."
		content := leading + "\n\n```json\n" + envelope + "\n```"
		if _, ok := CoordinatorCloseoutTranscriptNarrative(content); ok {
			t.Fatal("hybrid closeout must not project")
		}
	})

	t.Run("fenced json only projects synthesis", func(t *testing.T) {
		t.Parallel()
		content := "```json\n" + envelope + "\n```"
		got, ok := CoordinatorCloseoutTranscriptNarrative(content)
		if !ok || got != stub {
			t.Fatalf("narrative = %q, ok = %v", got, ok)
		}
	})

	t.Run("string cited_evidence entries do not parse", func(t *testing.T) {
		t.Parallel()
		content := `{"synthesis":"Yes.","cited_evidence":["pkg/foo.go:1"]}`
		if _, ok := ParseCoordinatorCompletionReport(content); ok {
			t.Fatal("string cited_evidence must not parse as closeout report")
		}
	})

	t.Run("string cited_evidence entries are unread", func(t *testing.T) {
		t.Parallel()
		read, ok := ReadCloseoutReport(`{"synthesis":"Yes.","cited_evidence":["pkg/foo.go:1"]}`, "")
		if !ok || read.Report.Synthesis != "Yes." || len(read.Report.CitedEvidence) != 0 {
			t.Fatalf("read = %+v ok=%v", read, ok)
		}
		if len(read.Unread) != 1 || read.Unread[0].Path != "cited_evidence[0]" || read.Unread[0].Want != "object" {
			t.Fatalf("unread = %+v, want the string entry named", read.Unread)
		}
	})

	t.Run("fenced json reads as its envelope", func(t *testing.T) {
		t.Parallel()
		envelope := `{"synthesis":"Report body.","cited_evidence":[{"path":"a.go","line":1,"excerpt":"x"}]}`
		read, ok := ReadCloseoutReport("```json\n"+envelope+"\n```", "")
		if !ok || read.Report.Synthesis != "Report body." || len(read.Report.CitedEvidence) != 1 || len(read.Unread) != 0 {
			t.Fatalf("read = %+v ok=%v", read, ok)
		}
	})

	t.Run("trailing junk after early-closed object salvages first value", func(t *testing.T) {
		t.Parallel()
		// The object closes after synthesis and the remaining fields follow it.
		malformed := `{"synthesis":"## Report\n\nDone.\n\n"}, "cited_evidence":[{"path":"weather_cli.py","line":1,"excerpt":"#!/usr/bin/env python3"}],"cited_urls":[],"artifact_ids":[]}`
		if _, ok := ParseCoordinatorCompletionReport(malformed); ok {
			t.Fatal("strict parse must reject trailing junk")
		}
		read, ok := ReadCloseoutReport(malformed, "")
		if !ok || read.Report.Synthesis != "## Report\n\nDone." {
			t.Fatalf("salvaged read = %+v ok=%v", read, ok)
		}
		raw, err := MarshalCoordinatorCompletionReport(read.Report)
		testutil.FailErr(t, "marshal salvaged report", err)
		got, ok := CoordinatorCloseoutTranscriptNarrative(raw)
		if !ok || got != "## Report\n\nDone." {
			t.Fatalf("narrative = %q ok=%v", got, ok)
		}
	})

	t.Run("poisoned nested envelope synthesis does not project", func(t *testing.T) {
		t.Parallel()
		// Synthesis holds a malformed envelope verbatim.
		inner := `{"synthesis":"## Report\n\nDone."}, "cited_evidence":[]}`
		wrapped, err := MarshalCoordinatorCompletionReport(CoordinatorCompletionReport{Synthesis: inner})
		testutil.FailErr(t, "MarshalCoordinatorCompletionReport failed", err)
		if _, ok := CoordinatorCloseoutTranscriptNarrative(wrapped); ok {
			t.Fatal("envelope-shaped synthesis must not project to the transcript")
		}
	})

	t.Run("usable synthesis salvages envelope draft", func(t *testing.T) {
		t.Parallel()
		malformed := `{"synthesis":"Clean narrative."}, "cited_evidence":[]}`
		if got := UsableCloseoutSynthesis(malformed); got != "Clean narrative." {
			t.Fatalf("UsableCloseoutSynthesis = %q", got)
		}
		if got := UsableCloseoutSynthesis(`{"not":"a closeout"}`); got != "" {
			t.Fatalf("non-closeout envelope must not invent prose: %q", got)
		}
		if got := UsableCloseoutSynthesis("Plain prose answer."); got != "Plain prose answer." {
			t.Fatalf("plain prose = %q", got)
		}
	})

	t.Run("usable synthesis separates declared trailers from prose code examples", func(t *testing.T) {
		t.Parallel()
		for _, metadata := range []string{
			`{"cited_evidence":[{"path":"greeting.py","line":4,"excerpt":"def format_greeting(name):"}]}`,
			`{"cited_urls":["https://example.com"],"artifact_ids":[]}`,
			`{"verification":{"method":"inspection","reason":"Documentation only."}}`,
		} {
			for _, info := range []string{"json", "JSON", "json closeout", ""} {
				input := "Design note written.\n\n```" + info + "\n" + metadata + "\n```"
				if got := UsableCloseoutSynthesis(input); got != "Design note written." {
					t.Fatalf("usable synthesis with %q trailer = %q", info, got)
				}
			}
		}
		for _, example := range []string{
			"```json\n{}\n```",
			"```json\n{\"name\":\"example\"}\n```",
			"```\n{\"name\":\"example\"}\n```",
			"```yaml\ncited_evidence: []\n```",
			"```js\n{\"cited_evidence\":[]}\n```",
		} {
			input := "Example configuration:\n\n" + example
			if got := UsableCloseoutSynthesis(input); got != input {
				t.Fatalf("ordinary code example changed: %q", got)
			}
		}
	})

	t.Run("an untagged report fence is read and hidden", func(t *testing.T) {
		t.Parallel()
		answer := "The implementation lives in dedicated packages ([docs/secrets.md:7](docs/secrets.md#L7)).\n\n```sh\n./task check\n```\n\nTools only ever receive a managed reference."
		trailer := "```\n{\"cited_evidence\":[{\"evidence\":\"read#2\"}],\"verification\":{\"method\":\"inspection\",\"reason\":\"Summary of docs/secrets.md as written; no runtime claims tested\"}}\n```"
		read, ok := ReadCloseoutReport(answer+"\n\n"+trailer, "")
		if !ok || read.Report.Synthesis != answer || len(read.Unread) > 0 {
			t.Fatalf("read = %+v ok=%v, want the answer above the fence and no unread members", read, ok)
		}
		if len(read.Report.CitedEvidence) != 1 || read.Report.CitedEvidence[0].Evidence != "read#2" || read.Report.Verification == nil {
			t.Fatalf("report = %+v, want the fence's citation and verification", read.Report)
		}
	})

	t.Run("plain prose does not project", func(t *testing.T) {
		t.Parallel()
		if _, ok := CoordinatorCloseoutTranscriptNarrative("Plain prose, no envelope."); ok {
			t.Fatal("non-envelope content must not project")
		}
	})
}
