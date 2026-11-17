package command

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func printedPageRun(plan toolsurface.Plan) *programRun {
	return &programRun{tc: tools.ToolContext{
		Turn:    tools.InvocationTurn{TurnToolPlan: plan},
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}}
}

func printPage(t *testing.T, run *programRun, class outputStreamClass) {
	t.Helper()
	page := evidence.DeliveredPage{Host: "www.example.test", Bytes: 612480, ContentType: "text/html; charset=utf-8"}
	tracker := &opProvenanceTracker{exchanges: []evidence.ExchangeProvenance{{
		External: true, ObservedURLs: []string{"https://www.example.test/guide/"}, WroteStdout: true, Page: &page,
	}}}
	run.finishOpTracker(t.Context(), tracker, class, true, outputStreamClass{}, false)
	run.statePrintedPages()
}

func TestPrintedRemotePageIsStatedOnlyFromTheProgramsOwnStdout(t *testing.T) {
	addressable := toolsurface.Compile([]string{"command", "fetch_url"}, nil)
	for name, tc := range map[string]struct {
		class outputStreamClass
		want  bool
	}{
		"program output": {outputStreamClass{topLevel: true}, true},
		"piped onward":   {outputStreamClass{piped: true}, false},
		"redirected":     {outputStreamClass{redirectPath: "page.html", redirectOperator: ">"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			run := printedPageRun(addressable)
			printPage(t, run, tc.class)
			if got := run.tc.Effects.Out.Facts.HasCode(toolrejection.HTTPRequestWebPageCode); got != tc.want {
				t.Fatalf("stated = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrintedRemotePageNeedsAnAddressableFetchURL(t *testing.T) {
	for name, tc := range map[string]struct {
		plan toolsurface.Plan
		want bool
	}{
		"immediate": {toolsurface.Compile([]string{"command", "fetch_url"}, nil), true},
		"deferred":  {toolsurface.Compile([]string{"command"}, []string{"fetch_url"}), true},
		"withheld":  {toolsurface.Compile([]string{"command"}, nil), false},
	} {
		t.Run(name, func(t *testing.T) {
			run := printedPageRun(tc.plan)
			printPage(t, run, outputStreamClass{topLevel: true})
			facts := run.tc.Effects.Out.Facts
			if got := facts.HasCode(toolrejection.HTTPRequestWebPageCode); got != tc.want {
				t.Fatalf("stated = %v, want %v", got, tc.want)
			}
			if !tc.want {
				return
			}
			details := facts.FeedbackFor(toolrejection.HTTPRequestWebPageCode).Details
			if details["host"] != "www.example.test" || details["bytes"] != "612480" || details["content_type"] != "text/html; charset=utf-8" {
				t.Fatalf("details = %+v", details)
			}
		})
	}
}
