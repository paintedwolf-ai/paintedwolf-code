package actioncard

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/pkg/api"
)

var activityStates = []string{"running", "waiting", "completed", "refused", "failed", "interrupted", "unknown"}

func randomActivity(r *rand.Rand, id string) api.CommandActivity {
	activity := api.CommandActivity{ID: id, Label: []string{"", "Run command", "Find files"}[r.IntN(3)], Subject: "subject", State: activityStates[r.IntN(len(activityStates))]}
	if r.IntN(2) == 0 {
		activity.StatusCode = intPtr(r.IntN(3))
	}
	if r.IntN(3) == 0 {
		activity.JobHandle = "job"
	}
	activity.ExecutionCount = r.IntN(5)
	activity.FailedCount = r.IntN(activity.ExecutionCount + 1)
	if r.IntN(4) != 0 {
		activity.ExecutionIDs = []string{"e"}
	}
	activity.Spans = []api.CommandActivitySpan{{SourceRef: "src", StartByte: r.IntN(8), EndByte: r.IntN(30)}}
	return activity
}

func randomResult(r *rand.Rand, tool string) api.ToolResult {
	result := api.ToolResult{Tool: tool, ToolCallID: "call", ToolArgs: map[string]any{"command": "go test ./... && go vet ./...", "id": "term-1"}}
	result.Outcome = []api.ToolResultOutcome{"", api.ToolResultOutcomeCompleted, api.ToolResultOutcomeRejected, api.ToolResultOutcomeError}[r.IntN(4)]
	if r.IntN(2) == 0 {
		result.Content = "output"
	}
	if r.IntN(3) == 0 {
		result.Codes = []string{"CODE"}
	}
	if r.IntN(3) == 0 {
		settled := time.Unix(100, 0)
		result.Invocation = &api.InvocationReceipt{Status: api.InvocationStatusCompleted, StartedAt: time.Unix(90, 0), SettledAt: &settled}
	}
	if r.IntN(4) == 0 {
		result.Process = &api.ToolProcessHandle{Handle: "term-1", Running: r.IntN(2) == 0}
	}
	return result
}

// checkBounded is the shape every card relies on, whatever the input.
func checkBounded(t *testing.T, d api.ActionDescriptor) {
	t.Helper()
	if d.Label == "" || d.Ran.Text == "" || d.Ran.Noun == "" {
		t.Fatalf("unnamed card %+v", d)
	}
	if d.Status != statusRunning && d.Status != statusDone && d.Status != statusError {
		t.Fatalf("status %q", d.Status)
	}
	if len(d.Facts) > maxFacts {
		t.Fatalf("%d facts", len(d.Facts))
	}
	positions := make([]int, len(d.Facts))
	for i, f := range d.Facts {
		positions[i] = slices.Index(factOrder, f.Kind)
		if positions[i] < 0 {
			t.Fatalf("unknown fact kind %q", f.Kind)
		}
	}
	if !slices.IsSorted(positions) || len(slices.Compact(slices.Clone(positions))) != len(positions) {
		t.Fatalf("facts out of order or repeated: %v", kinds(d.Facts))
	}
	for _, kind := range d.Row {
		if _, ok := fact(d.Facts, kind); !ok {
			t.Fatalf("row names %q, which the card does not show", kind)
		}
	}
	order := []string{linkInput, linkOutput, linkReceipt}
	last := -1
	for _, link := range d.Links {
		at := slices.Index(order, link.Kind)
		if at <= last {
			t.Fatalf("links out of order: %+v", d.Links)
		}
		last = at
	}
}

func TestEveryDescriptorIsBounded(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 600 {
		a, b := randomActivity(r, "a"), randomActivity(r, "b")
		activities := []api.CommandActivity{a}
		if r.IntN(2) == 0 {
			activities = append(activities, b)
		}
		settled := time.Unix(200, 0)
		p := program(time.Unix(100, 0), &settled, activities...)
		var marks []api.RedactedSpan
		if r.IntN(4) == 0 {
			marks = []api.RedactedSpan{{Field: "tool_result.tool_args.command", Start: 0, Length: 3}}
		}
		checkBounded(t, ForActivity(p, a, randomResult(r, "command"), marks))
		for _, tool := range Tools() {
			if d := ForResult(randomResult(r, tool), nil); d != nil {
				checkBounded(t, *d)
			}
		}
	}
}

// Presenters read typed facts only: two results that differ only in their
// output text get the same card.
func TestOutputTextNeverChangesACard(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 200 {
		for _, tool := range Tools() {
			result := randomResult(r, tool)
			result.Content = "first output"
			other := result
			other.Content = "entirely different output: FAILED exit 1 refused"
			if a, b := ForResult(result, nil), ForResult(other, nil); !reflect.DeepEqual(a, b) {
				t.Fatalf("%s: output text changed the card:\n%+v\n%+v", tool, a, b)
			}
		}
	}
}

// Every tool with a presenter takes its catalog label and, when its family
// names no target of its own, the host's screened target.
func TestPresentedToolsUseTheirCatalogLabel(t *testing.T) {
	for _, tool := range Tools() {
		result := api.ToolResult{Tool: tool, DisplaySubject: "screened target", Content: "x"}
		d := ForResult(result, nil)
		if d == nil {
			t.Fatalf("%s: no card", tool)
		}
		if d.Label != toolpresentation.Label(tool) || d.Label == "" {
			t.Fatalf("%s: label %q", tool, d.Label)
		}
		if d.Ran.Text != "screened target" {
			t.Fatalf("%s: ran %q", tool, d.Ran.Text)
		}
	}
}
