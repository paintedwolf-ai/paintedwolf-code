package toolfeedback

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// Grounding repair names recall only when the request offered it; producer
// data claiming otherwise cannot change that.
func TestGroundingRejectionsNameRecallOnlyWhenOffered(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tc := range []struct {
		code, tool, fallback string
		data                 map[string]any
	}{
		{"SUBMIT_VERDICT_CITATION_UNGROUNDED", "submit_verdict", "Cite only handles shown", map[string]any{"review_ungrounded_sample": []string{"read#9"}}},
		{"SUBMIT_VERDICT_CITATIONS_REQUIRED", "submit_verdict", "Cite only handles shown", nil},
		{"FINDING_UNGROUNDED", "record_finding", "acquire missing evidence through an available tool", map[string]any{"offenders_sample": "grep#9"}},
		{"SURFACE_NOTE_UNGROUNDED", "surface_note", "observe first", nil},
		{"SURFACE_NOTE_HANDLE_UNGROUND", "surface_note", "re-read the path", map[string]any{"offenders_sample": "read#9", "observed_paths_sample": "src/a.go"}},
	} {
		for _, offered := range [][]string{{"read", "recall"}, {"read"}, nil} {
			callable := len(offered) == 2
			t.Run(tc.code+"/"+strings.Join(offered, ","), func(t *testing.T) {
				ctx := t.Context()
				if offered != nil {
					ctx = tools.WithRecoveryTools(ctx, offered)
				}
				data := map[string]any{"can_recall": !callable}
				for k, v := range tc.data {
					data[k] = v
				}
				err := bp.RejectObservation(ctx, tc.tool, "coordinator", nil, &toolrejection.ToolReject{Code: tc.code, Data: data})
				refusal, ok := guidance.RefusalFromError(err)
				if !ok || refusal.Copy == nil {
					t.Fatalf("missing evaluated refusal: %v", err)
				}
				fix := refusal.Copy["fix"]
				if callable && !strings.Contains(fix, `recall(query="`) {
					t.Fatalf("offered recall missing from repair: %q", fix)
				}
				if !callable && strings.Contains(fix, "recall") {
					t.Fatalf("repair names recall without offering it: %q", fix)
				}
				if !callable && !strings.Contains(fix, tc.fallback) {
					t.Fatalf("fallback %q missing: %q", tc.fallback, fix)
				}
			})
		}
	}
}
