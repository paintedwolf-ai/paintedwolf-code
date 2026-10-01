package events

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestDebounceForTopic(t *testing.T) {
	cases := []struct {
		topic api.EventTopic
		want  time.Duration
	}{
		{api.EventTopicBoard, DebounceBoard},
		{api.EventTopicSession, DebounceSession},
		{api.EventTopicCost, DebounceCost},
		{api.EventTopicLLM, DebounceLLM},
		{api.EventTopicProviders, DebounceLLM},
		{api.EventTopicModelPolicy, DebounceLLM},
		{api.EventTopicSettings, DebounceLLM},
		{api.EventTopicMessage, DebounceImmediate},
		{api.EventTopicWorker, DebounceImmediate},
		{api.EventTopicDelegation, DebounceImmediate},
		{api.EventTopicCheckpoint, DebounceImmediate},
		{api.EventTopicScan, DebounceImmediate},
		{api.EventTopicWorkflow, DebounceImmediate},
		{api.EventTopicGrounding, DebounceImmediate},
		{api.EventTopicFindings, DebounceImmediate},
		{api.EventTopicProgress, DebounceImmediate},
		{api.EventTopicAgentPresence, DebounceImmediate},
	}

	for _, tc := range cases {
		t.Run(string(tc.topic), func(t *testing.T) {
			if got := DebounceForTopic(tc.topic); got != tc.want {
				t.Fatalf("DebounceForTopic(%q)=%v want %v", tc.topic, got, tc.want)
			}
		})
	}
}

// Unrevisioned workflow events (workflow.persisted, gate_pending) share a bare
// session key, so any window would coalesce them with run transitions.
func TestWorkflowTopicStaysImmediateSoUnrevisionedEventsCannotCoalesce(t *testing.T) {
	if got := DebounceForTopic(api.EventTopicWorkflow); got != DebounceImmediate {
		t.Fatalf("DebounceForTopic(workflow)=%v want %v: unrevisioned workflow events "+
			"(workflow.persisted, gate_pending) carry no facet or revision, so a debounce "+
			"window lets them coalesce with run transitions on the same session key", got, DebounceImmediate)
	}
}
