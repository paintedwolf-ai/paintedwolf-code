package session

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminalEmptyResponseRendersContextualOAR(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	mgr.ToolPolicy.Pipeline.EnableAnchor(oar.AnchorContentOutput)
	for mask := 0; mask < 8; mask++ {
		obs := oar.ContentObservation{}
		if mask&1 != 0 {
			obs.EmptyResponse = &oar.EmptyResponseObservation{ToolsCallable: mask&2 != 0, StructuredOutput: mask&4 != 0}
		}
		reject, blocked, _, _, err := mgr.ToolPolicy.ContentBlock(t.Context(), &api.Session{ID: "empty-response"}, oar.AnchorContentOutput, obs, "", nil)
		testutil.FailErr(t, "evaluate terminal-empty policy", err)
		if blocked != (mask&1 != 0) {
			t.Fatalf("mask%d: blocked=%v", mask, blocked)
		}
		if !blocked {
			continue
		}
		if reject.Code() != "MODEL_RESPONSE_EMPTY" {
			t.Fatalf("wrong rejection: %+v", reject)
		}
		for _, clause := range []struct {
			text string
			want bool
		}{
			{"structured answer", mask&4 != 0},
			{"submit a native call", mask&4 == 0 && mask&2 != 0},
			{"Tool calls are unavailable", mask&4 == 0 && mask&2 == 0},
		} {
			if strings.Contains(reject.Body, clause.text) != clause.want {
				t.Fatalf("mask%d: clause %q mismatch: %s", mask, clause.text, reject.Body)
			}
		}
	}
}
