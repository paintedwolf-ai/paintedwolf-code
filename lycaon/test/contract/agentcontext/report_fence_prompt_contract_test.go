package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"strings"
)

// The workflow's report section shows a literal fence, and the host reads that
// example whole: the fence a coordinator copies is the fence the host hides.
func TestReportSectionShowsTheFenceTheHostReads(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	vars := inject.ActiveWorkflowInjectToMap(inject.ActiveWorkflowInjectData{
		WorkflowID: "contract", CurrentPhase: "report", ReportDocumentEnabled: true,
		Phases: []inject.ActiveWorkflowPhaseView{{ID: "report", IsCurrent: true}},
	}, nil, nil)
	out, err := engine.Render(context.Background(), "guidance/active-workflow.md", vars)
	contractcheck.FailErr(t, "render the report section", err)
	fences := jsonFences(out)
	if len(fences) != 1 {
		t.Fatalf("report section shows %d json fences:\n%s", len(fences), out)
	}
	read := guidance.ReadCloseoutReport("The answer.\n\n"+fences[0], guidance.RetainedCloseout{}, true, citationmd.Options{})
	if read.Draft.Document == nil || read.Draft.Synthesis != "The answer." {
		t.Fatalf("the host does not read this fence as a report:\n%s", fences[0])
	}
	if len(read.Unread) > 0 {
		t.Fatalf("the fence shows members the host does not read: %+v", read.Unread)
	}
}

// An ordinary completion reply teaches no fence: citations are inline.
func TestCompletionReplyTeachesNoFence(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for name, vars := range map[string]map[string]any{
		"one root":    {"root_count": 1},
		"every field": {"root_count": 2, "web_search_enabled": true, "profile_has_summarize": true, "profile_has_capture_page": true},
	} {
		out, err := engine.Render(context.Background(), "partials/coordinator-final-report.md", vars)
		contractcheck.FailErr(t, "render "+name, err)
		if fences := jsonFences(out); len(fences) != 0 {
			t.Fatalf("%s shows a json fence:\n%s", name, out)
		}
	}
}

// jsonFences returns each ```json block in a rendered prompt, fences included.
func jsonFences(prompt string) []string {
	const open, closer = "```json\n", "\n```"
	var out []string
	for {
		start := strings.Index(prompt, open)
		if start < 0 {
			return out
		}
		end := strings.Index(prompt[start+len(open):], closer)
		if end < 0 {
			return out
		}
		stop := start + len(open) + end + len(closer)
		out = append(out, prompt[start:stop])
		prompt = prompt[stop:]
	}
}
