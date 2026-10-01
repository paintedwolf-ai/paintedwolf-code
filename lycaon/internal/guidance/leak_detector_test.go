package guidance_test

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/evidence"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProseLeaks_ignoresUnobservedPathLine(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", nil)
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, nil, nil)
	leaks := guidance.ProseLeaks("Bug at internal/auth/handler.go:42 in handler", ev, typed)
	if len(leaks) != 0 {
		t.Fatalf("leaks = %+v want none for unobserved path", leaks)
	}
}

func TestProseLeaks_duplicationWhenAlsoInTyped(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", readHandleMessages("internal/auth/handler.go", 42, "return nil"))
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, []guidance.WorkerFindingInput{
		{Path: "internal/auth/handler.go", Evidence: "read#1", Line: 42},
	}, nil)
	leaks := guidance.ProseLeaks("Also noted internal/auth/handler.go:42 in brief", ev, typed)
	if len(leaks) != 1 || !leaks[0].AlsoInTyped {
		t.Fatalf("leaks = %+v want duplication", leaks)
	}
}

func TestProseLeaks_observedNotInTypedIsAdvisory(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", readHandleMessages("f.go", 42, "return nil"))
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, []guidance.WorkerFindingInput{
		{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return nil"},
	}, nil)
	eval := guidance.EvaluateWorkerProseLeaks(guidance.WorkerNarrativeInput{
		Brief: "Also saw src/other.go:10 during survey",
	}, ev, typed)
	if len(eval.AdvisoryLeakTokens) != 0 {
		t.Fatalf("advisory = %v want none for unobserved src/other.go", eval.AdvisoryLeakTokens)
	}
	ev2 := ledgertest.BuildFromMessages("", readHandleMessages("f.go", 42, "return nil"))
	typed2 := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev2, nil, nil)
	eval2 := guidance.EvaluateWorkerProseLeaks(guidance.WorkerNarrativeInput{
		Brief: "Issue in f.go:42 outside typed findings",
	}, ev2, typed2)
	if len(eval2.AdvisoryLeakTokens) != 1 || eval2.AdvisoryLeakTokens[0] != "f.go:42" {
		t.Fatalf("eval2 = %+v want advisory leak on observed f.go:42", eval2)
	}
}

func TestProseLeaks_ignoresUnobservedURL(t *testing.T) {
	ev := evidence.Ledger{}
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, nil, nil)
	eval := guidance.EvaluateWorkerProseLeaks(guidance.WorkerNarrativeInput{
		ObjectivesMet: []string{"Fetched https://example.com/docs for context"},
	}, ev, typed)
	if len(eval.AdvisoryLeakTokens) != 0 {
		t.Fatalf("eval = %+v want no leak for unobserved URL", eval)
	}
}

func TestProseLeaks_scrapedPathIsAdvisory(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "command", ID: "c1", Args: map[string]any{"command": "ls"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: "internal/scraped/path.go\n",
		}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, nil, nil)
	eval := guidance.EvaluateWorkerProseLeaks(guidance.WorkerNarrativeInput{
		Brief: "Output mentioned internal/scraped/path.go",
	}, ev, typed)
	if len(eval.AdvisoryLeakTokens) != 1 {
		t.Fatalf("advisory = %v want scraped path advisory", eval.AdvisoryLeakTokens)
	}
}

func TestProseLeaks_ignoresPathShapedNonPath(t *testing.T) {
	ev := evidence.Ledger{}
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, nil, nil)
	for _, prose := range []string{
		"read/grep are the main tools",
		"import internal/api for types",
		"ratio a/b is acceptable",
	} {
		if leaks := guidance.ProseLeaks(prose, ev, typed); len(leaks) != 0 {
			t.Fatalf("prose %q leaks = %+v want none", prose, leaks)
		}
	}
}

func TestProseLeaks_windowsPathObservedMembership(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": `C:\repo\src\main.go`, "offset": 10, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"C:\\repo\\src\\main.go","content":"10| func main()","offset":10,"end_line":10,"limit":1}`,
		}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	typed := guidance.BuildWorkerTypedChannel(evidence.CitationRoots{}, ev, nil, nil)
	eval := guidance.EvaluateWorkerProseLeaks(guidance.WorkerNarrativeInput{
		Brief: "See c:/repo/src/main.go:10 in the log",
	}, ev, typed)
	if len(eval.AdvisoryLeakTokens) != 1 {
		t.Fatalf("eval = %+v want Windows path advisory leak", eval)
	}
}

func TestEvaluateWorkerCitations_proseLeakIsAdvisoryNotBlocking(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", readHandleMessages("f.go", 42, "return nil"))
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, nil, nil, guidance.WorkerNarrativeInput{
		Brief: "Issue in f.go:42 outside typed findings",
	}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want no blocking code — prose placement is advisory", eval)
	}
	if len(eval.ProseAdvisoryTokens) != 1 || eval.ProseAdvisoryTokens[0] != "f.go:42" {
		t.Fatalf("advisory = %v want f.go:42 surfaced for review", eval.ProseAdvisoryTokens)
	}
}

func TestEvaluateWorkerCitations_unobservedProsePasses(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", readHandleMessages("f.go", 42, "return nil"))
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return nil"},
	}, nil, guidance.WorkerNarrativeInput{
		Brief: "Issue in src/other.go:10 without typed finding",
	}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want pass for unobserved prose", eval)
	}
}

func TestEvaluateWorkerCitations_proseDuplicationPasses(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", readHandleMessages("f.go", 42, "return nil"))
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return nil"},
	}, nil, guidance.WorkerNarrativeInput{
		Brief: "Traced f.go:42 during survey",
	}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want pass with duplication nudge", eval)
	}
	if len(eval.ProseDuplicateTokens) != 1 || eval.ProseDuplicateTokens[0] != "f.go:42" {
		t.Fatalf("duplicates = %v", eval.ProseDuplicateTokens)
	}
}

func readHandleMessages(path string, line int, body string) []api.Message {
	readJSON := `{"path":` + `"` + path + `"` + `,"content":` + `"` + fmt.Sprintf("%d|  %s", line, body) + `"` + `,"offset":` + fmt.Sprintf("%d", line) + `,"end_line":` + fmt.Sprintf("%d", line) + `,"limit":1}`
	return []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": path, "offset": line, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	}
}
