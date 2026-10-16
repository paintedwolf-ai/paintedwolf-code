package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEmptyRecoveryUsesFrozenRequestEligibility(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		req := modelcall.CompletionRequest{}
		if mask&1 != 0 {
			req.Tools = []tools.ToolMeta{{Name: "read"}}
		}
		if mask&2 != 0 {
			req.ToolUse = modelcall.ToolUseForbidden
		}
		if mask&4 != 0 {
			req.ResponseFormat = &modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONObject}
		}
		sess := &api.Session{ID: "session"}
		if mask&8 != 0 {
			sess.ParentSessionID = "parent"
		}
		calls := 0
		turn := &modelTurn{Closeout: &turnCloseout{Deps: CloseoutDeps{EvaluateContentAnchor: func(_ context.Context, s *api.Session, anchor string, obs oar.ContentObservation, _ string, _ map[string]any) (*guidance.Refusal, bool, string, bool) {
			calls++
			if s != sess || anchor != oar.AnchorContentOutput || obs.EmptyResponse == nil || obs.EmptyResponse.ToolsCallable != req.ToolsCallable() || obs.EmptyResponse.StructuredOutput != (req.ResponseFormat != nil) {
				t.Fatalf("mask%d: observation=%+v", mask, obs)
			}
			if len(obs.Segments) != 0 || len(obs.TextActionTools) != 0 {
				t.Fatal("provider text entered recovery observation")
			}
			return guidance.NewRefusal("MODEL_RESPONSE_EMPTY", "host recovery"), true, "", false
		}}}}
		render := turn.emptyResponseRecovery(sess, req)
		body, err := render(t.Context())
		testutil.FailErr(t, "render recovery", err)
		if body != "host recovery" || calls != 1 {
			t.Fatalf("body=%q calls=%d", body, calls)
		}
	}
}
