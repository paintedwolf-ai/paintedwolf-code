package guidance_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBindObservedSampleBindsObservedPathsAndURLs(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "src/a.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/a.go","content":"1|package main","offset":1,"end_line":1,"limit":1}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "web_search", Args: map[string]any{"query": "x"}}}},
		{Role: api.MessageRoleTool, Content: `{"results":[{"url":"https://example.com/a"},{"url":"https://example.com/b"}],"provider":"brave"}`},
	}
	ev := ledgertest.BuildFromMessages("", msgs)

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: "answer"}, ev, evidence.CitationRoots{})

	if len(report.CitedEvidence) == 0 {
		t.Fatalf("expected observed path bound to cited_evidence, got none")
	}
	observedPaths := map[string]struct{}{}
	for _, p := range evidence.ObservedPathsSorted(ev) {
		observedPaths[p] = struct{}{}
	}
	for _, c := range report.CitedEvidence {
		if _, ok := observedPaths[c.Path]; !ok {
			t.Fatalf("cited_evidence %q not in observed set %v", c.Path, evidence.ObservedPathsSorted(ev))
		}
	}
	observedURLs := map[string]struct{}{}
	for _, u := range evidence.ObservedURLsSorted(ev) {
		observedURLs[u] = struct{}{}
	}
	if len(report.CitedURLs) == 0 {
		t.Fatalf("expected observed URLs bound to cited_urls, got none")
	}
	for _, u := range report.CitedURLs {
		if _, ok := observedURLs[u]; !ok {
			t.Fatalf("cited_url %q not in observed set %v", u, evidence.ObservedURLsSorted(ev))
		}
	}
}

// Whole-file observations remain available as typed host-attached citations.
func TestBuildObservedAutobindGroundingSurfacesTypedCitedEvidence(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "src/a.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/a.go","content":"1|package main","offset":1,"end_line":1,"limit":1}`},
	}
	ev := ledgertest.BuildFromMessages("", msgs)

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: "answer"}, ev, evidence.CitationRoots{})
	if len(report.CitedEvidence) == 0 {
		t.Fatalf("fixture bound no observed cited_evidence")
	}

	g := guidance.BuildObservedAutobindGrounding(evidence.CitationRoots{}, ev, report)
	if g == nil {
		t.Fatal("expected a grounding envelope")
	}
	if g.Traced || !g.HostAssembled {
		t.Fatalf("host-assembled grounding must be traced=false host_assembled=true, got %+v", g)
	}
	if len(g.CitedEvidence) == 0 {
		t.Fatalf("wire cited_evidence empty — chicklet would be hidden; grounding=%+v", g)
	}
	for _, c := range g.CitedEvidence {
		if c.Handle == "" {
			t.Fatalf("bound cited_evidence missing resolved handle: %+v", c)
		}
		if c.Verdict != api.CitationVerdictMatched {
			t.Fatalf("observed cited_evidence verdict=%q want matched", c.Verdict)
		}
	}
}

func TestBindObservedSampleRespectsCap(t *testing.T) {
	var msgs []api.Message
	total := guidance.ObservedSampleCap + 6
	for i := 0; i < total; i++ {
		path := fmt.Sprintf("src/pkg%02d/file.go", i)
		msgs = append(msgs,
			api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": path}}}},
			api.Message{Role: api.MessageRoleTool, Content: fmt.Sprintf(`{"path":%q,"content":"1|package pkg","offset":1,"end_line":1,"limit":1}`, path)},
		)
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	if got := len(evidence.ObservedPathsSorted(ev)); got <= guidance.ObservedSampleCap {
		t.Fatalf("fixture only produced %d observed paths, need > cap %d", got, guidance.ObservedSampleCap)
	}

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: "answer"}, ev, evidence.CitationRoots{})
	if len(report.CitedEvidence) > guidance.ObservedSampleCap {
		t.Fatalf("cited_evidence = %d, exceeds cap %d", len(report.CitedEvidence), guidance.ObservedSampleCap)
	}
}

// Observed paths mentioned in prose take priority within the sample cap.
func TestBindObservedSamplePrefersPathsMentionedInSynthesis(t *testing.T) {
	var msgs []api.Message
	for i := 0; i < guidance.ObservedSampleCap+8; i++ {
		path := fmt.Sprintf("aaa/early%02d.md", i) // sorts before lycaon/…
		msgs = append(msgs,
			api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": path}}}},
			api.Message{Role: api.MessageRoleTool, Content: fmt.Sprintf(`{"path":%q,"content":"1|early","offset":1,"end_line":1,"limit":1}`, path)},
		)
	}
	target := "lycaon/internal/db/schema.sql"
	msgs = append(msgs,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": target}}}},
		api.Message{Role: api.MessageRoleTool, Content: fmt.Sprintf(
			`{"path":%q,"content":"440|-- comment","offset":440,"end_line":440,"limit":1}`, target)},
	)
	ev := ledgertest.BuildFromMessages("", msgs)
	prose := "See `" + target + ":440` for the schema comment."

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: prose}, ev, evidence.CitationRoots{})
	if len(report.CitedEvidence) == 0 {
		t.Fatal("expected cited_evidence")
	}
	var hit *guidance.CoordinatorCitedEvidence
	for i := range report.CitedEvidence {
		if report.CitedEvidence[i].Path == target {
			hit = &report.CitedEvidence[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("synthesis-mentioned %q missing from cited_evidence %v", target, report.CitedEvidence)
	}
	if hit.Line != 440 {
		t.Fatalf("cited line = %d, want 440 from prose", hit.Line)
	}
}

func TestBindObservedSamplePrefersUniquePathSuffixInSynthesis(t *testing.T) {
	full := "lycaon/internal/coordinator/promptloop/stream.go"
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "aaa/noise.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"aaa/noise.go","content":"1|x","offset":1,"end_line":1,"limit":1}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": full}}}},
		{Role: api.MessageRoleTool, Content: fmt.Sprintf(`{"path":%q,"content":"100|func completeStream","offset":100,"end_line":100,"limit":1}`, full)},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	prose := "lint: `internal/coordinator/promptloop/stream.go:100` is too long"

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: prose}, ev, evidence.CitationRoots{})
	var hit *guidance.CoordinatorCitedEvidence
	for i := range report.CitedEvidence {
		if report.CitedEvidence[i].Path == full {
			hit = &report.CitedEvidence[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("unique suffix mention did not bind %q; cited=%v", full, report.CitedEvidence)
	}
	if hit.Line != 100 {
		t.Fatalf("line = %d want 100", hit.Line)
	}
}

func TestSelectObservedPathSampleSkipsAlreadyCited(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "src/a.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/a.go","content":"1|a","offset":1,"end_line":1,"limit":1}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "src/b.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/b.go","content":"1|b","offset":1,"end_line":1,"limit":1}`},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	got := guidance.SelectObservedPathSample(ev, evidence.CitationRoots{}, "", map[string]struct{}{"src/a.go": {}})
	for _, sample := range got {
		if sample.Path == "src/a.go" {
			t.Fatalf("already-cited path returned: %+v", got)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected fill from remaining observed paths")
	}
}

func TestBindObservedSampleDoesNotInventUnobservedPaths(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "src/observed.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"src/observed.go","content":"1|ok","offset":1,"end_line":1,"limit":1}`},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	prose := "Also see `lycaon/never/observed.go:9` which was not read this turn."

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: prose}, ev, evidence.CitationRoots{})
	for _, c := range report.CitedEvidence {
		if c.Path == "lycaon/never/observed.go" {
			t.Fatalf("invented unobserved path in cited_evidence: %+v", report.CitedEvidence)
		}
	}
	if len(report.CitedEvidence) != 1 || report.CitedEvidence[0].Path != "src/observed.go" {
		t.Fatalf("expected only observed path, got %+v", report.CitedEvidence)
	}
}

func TestBindObservedSamplePrefersURLsMentionedInSynthesis(t *testing.T) {
	var results []string
	for i := 0; i < guidance.ObservedSampleCap+4; i++ {
		results = append(results, fmt.Sprintf(`{"url":"https://early.example.com/%02d"}`, i))
	}
	target := "https://status.example.com/incident/42"
	results = append(results, fmt.Sprintf(`{"url":%q}`, target))
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "web_search", Args: map[string]any{"query": "x"}}}},
		{Role: api.MessageRoleTool, Content: `{"results":[` + strings.Join(results, ",") + `],"provider":"brave"}`},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	prose := "Root cause write-up is at " + target + " from the vendor status page."

	report := guidance.BindObservedSample(guidance.CoordinatorCompletionReport{Synthesis: prose}, ev, evidence.CitationRoots{})
	found := false
	for _, u := range report.CitedURLs {
		if u == target {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("synthesis-mentioned URL missing from cited_urls %v", report.CitedURLs)
	}
}
