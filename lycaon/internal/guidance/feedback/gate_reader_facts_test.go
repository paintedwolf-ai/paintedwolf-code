package feedback

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A gate leaf reads the turn reader's facts as top-level variables beside
// `context`; without them a reader fact is absent and its guard is false.
func TestGateSignalTextReadsReaderFacts(t *testing.T) {
	const tmpl = `{% if profile_has_task %}held{% elif profile_can_request.task %}requestable{% else %}absent{% endif %} {{ context.review_agents }}`
	for _, tc := range []struct {
		name  string
		facts map[string]any
		want  string
	}{
		{name: "held", facts: map[string]any{"profile_has_task": true}, want: "held r"},
		{name: "requestable", facts: map[string]any{"profile_has_task": false, "profile_can_request": map[string]any{"task": true}}, want: "requestable r"},
		{name: "no reader", facts: nil, want: "absent r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extras := WithReaderFacts(map[string]any{"review_agents": "r"}, tc.facts)
			got, err := renderGateSignalText(t.Context(), tmpl, GateFeedbackContext(WorkflowEvaluationContext{}, extras))
			testutil.FailErr(t, "renderGateSignalText failed", err)
			if got != tc.want {
				t.Fatalf("rendered %q, want %q", got, tc.want)
			}
		})
	}
}
