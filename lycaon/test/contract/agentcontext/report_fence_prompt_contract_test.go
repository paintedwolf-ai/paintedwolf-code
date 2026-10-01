package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every prompt that teaches the closeout's report fence shows a literal one,
// and the host reads that example whole: the fence a coordinator copies is the
// fence the host hides and grounds.
func TestCloseoutPromptsShowTheReportFenceTheHostReads(t *testing.T) {
	t.Parallel()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()
	rendered := map[string]string{}
	for name, vars := range map[string]map[string]any{
		"final report, one root":  {"root_count": 1},
		"final report, two roots": {"root_count": 2},
		"final report, every field": {
			"root_count": 2, "web_search_enabled": true,
			"profile_has_summarize": true, "profile_has_capture_page": true,
		},
	} {
		out, err := engine.Render(ctx, "partials/coordinator-final-report.md", vars)
		contractcheck.FailErr(t, "render "+name, err)
		rendered[name] = out
	}
	kickVars := map[string]any{}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(kickVars))
	kick, err := engine.RenderKick(ctx, "coordinator-closeout", kickVars)
	contractcheck.FailErr(t, "render forced closeout", err)
	rendered["forced closeout"] = kick

	for name, out := range rendered {
		fences := jsonFences(out)
		if len(fences) == 0 {
			t.Fatalf("%s shows no json report fence:\n%s", name, out)
		}
		for _, fence := range fences {
			read, ok := guidance.ReadCloseoutReport("The answer.\n\n"+fence, "")
			if !ok || read.Report.Synthesis != "The answer." {
				t.Fatalf("%s shows a fence the host does not read as a report:\n%s", name, fence)
			}
			if len(read.Unread) > 0 {
				t.Fatalf("%s shows report members the host does not read: %+v", name, read.Unread)
			}
			if len(read.Report.CitedEvidence) == 0 {
				t.Fatalf("%s shows a fence without a citation:\n%s", name, fence)
			}
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
