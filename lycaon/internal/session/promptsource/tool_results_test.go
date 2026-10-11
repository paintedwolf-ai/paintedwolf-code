package promptsource

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolResultsRequireSuccessfulStructuredDispatchForEnqueueFeedback(t *testing.T) {
	results := &ToolResults{Batch: batchcontrol.New(nil, nil, nil, nil)}
	session := &api.Session{ID: "session"}
	for _, tc := range []struct {
		name, tool string
		succeeded  bool
		dispatch   *api.WorkerDispatch
		wantQueued bool
	}{
		{"dispatch", "task", true, &api.WorkerDispatch{WorkerID: " worker-1 "}, true},
		{"failed dispatch", "task", false, &api.WorkerDispatch{WorkerID: "worker-1"}, false},
		{"prose without dispatch", "task", true, nil, false},
		{"blank worker", "task", true, &api.WorkerDispatch{WorkerID: "  "}, false},
		{"different operation", "read", true, &api.WorkerDispatch{WorkerID: "worker-1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &tools.ToolInvocationOut{Dispatch: tc.dispatch, Facts: guidance.ToolResultFacts{}.WithCode("PRODUCER_FACT")}
			output := "worker-1 was queued"
			got := results.AfterTool(t.Context(), session, tc.tool, map[string]any{"agent_type": "implementer"}, output, tc.succeeded, out)
			if got != output || !out.Facts.HasCode("PRODUCER_FACT") {
				t.Fatalf("producer result changed: %q %+v", got, out.Facts)
			}
			if out.Facts.HasCode("BANNER_TASK_QUEUED") != tc.wantQueued {
				t.Fatalf("enqueue feedback=%+v", out.Facts)
			}
			if !tc.wantQueued {
				if out.Completion != nil {
					t.Fatalf("unconfirmed enqueue published completion: %+v", out.Completion)
				}
				return
			}
			want := &api.ToolCompletion{Operation: "worker_dispatch", State: "enqueued", ResourceKind: "worker", ResourceID: "worker-1"}
			if !reflect.DeepEqual(out.Completion, want) {
				t.Fatalf("completion=%+v, want %+v", out.Completion, want)
			}
			feedback := out.Facts.Feedback[len(out.Facts.Feedback)-1]
			if feedback.Subject == nil || feedback.Subject.Kind != "worker" || feedback.Subject.ID != "worker-1" || feedback.Details["agent_type"] != "implementer" || feedback.Details["job_id"] != "worker-1" {
				t.Fatalf("feedback lost dispatch coordinates: %+v", feedback)
			}
		})
	}
}

func TestToolResultsWithoutSessionOrRuntimePreserveProducerFacts(t *testing.T) {
	facts := guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeRejected}.WithCode("PRODUCER_REFUSAL")
	for _, tc := range []struct {
		results *ToolResults
		session *api.Session
	}{
		{nil, &api.Session{ID: "session"}},
		{&ToolResults{}, nil},
	} {
		output, got := tc.results.EnrichTool(t.Context(), tc.session, "task", nil, "producer output", facts, 0)
		if output != "producer output" || !reflect.DeepEqual(got, facts) {
			t.Fatalf("unavailable enrichment changed producer result: %q %+v", output, got)
		}
	}
}
