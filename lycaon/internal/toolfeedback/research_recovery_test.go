package toolfeedback

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFetchTransferGuidanceUsesFrozenProgramReach(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, code := range []string{"FETCH_URL_TYPE_UNSUPPORTED", "FETCH_URL_MODE_INVALID", "FETCH_URL_DEST_INVALID", "FETCH_URL_DEST_REQUIRED", "FETCH_URL_DEST_DENIED"} {
		for _, state := range []struct {
			name          string
			command, curl bool
		}{
			{"reader only", false, false}, {"program excluded", true, false}, {"transfer capable", true, true},
		} {
			t.Run(code+"/"+state.name, func(t *testing.T) {
				offered := []string{"fetch_url"}
				if state.command {
					offered = append(offered, "command")
				}
				reach := toolreach.Offered(offered).WithPrograms(func(name string) bool { return state.curl && name == "curl" })
				ctx := tools.WithCallReach(t.Context(), reach)
				err := bp.RejectObservation(ctx, "fetch_url", "web_research", nil, &toolrejection.ToolReject{Code: code, Data: map[string]any{"content_type": "application/zip", "reason": "content_type", "mode": "unknown", "path": "asset.zip"}})
				refusal, ok := guidance.RefusalFromError(err)
				if !ok || refusal.Copy == nil {
					t.Fatalf("missing recovery: %v", err)
				}
				calls, _ := refusal.Facts.FeedbackFor(code).Details[api.ToolFeedbackNextCalls].([]toolreach.Call)
				if (len(calls) > 0) != state.curl {
					t.Fatalf("calls=%v for %+v", calls, state)
				}
				fix := refusal.Copy["fix"]
				if strings.Contains(fix, "curl") != state.curl {
					t.Fatalf("unreachable transfer instruction: %s", fix)
				}
				for _, call := range calls {
					if call.Tool != "command" || call.Program != "curl" || call.Args["command"] != "curl --help" {
						t.Fatalf("unexpected recovery call: %+v", call)
					}
				}
			})
		}
	}
}
