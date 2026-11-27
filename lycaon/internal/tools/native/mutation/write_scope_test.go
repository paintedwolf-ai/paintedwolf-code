package mutation

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestTouchCoordinatorWriteScopeRefusal(t *testing.T) {
	for _, name := range []string{"no previous effect", "previous operand committed"} {
		t.Run(name, func(t *testing.T) {
			const profile = toolcontract.CoordinatorProfileID
			boundary := nativefixture.Boundary(t)
			boundary.SetPathScopes(sandbox.PathScopeRegistry{
				toolcontract.CoordinatorProductWriteScope: {
					Write: []string{"allowed.txt", "later.txt"},
					Deny:  []string{"denied.txt"},
				},
			})
			h := newHarnessWithBoundary(t, nil, boundary)
			h.tc.Identity.Agent = profile
			h.tc.Turn.TurnSurfaceID = toolcontract.SurfaceImplementInvestigate
			words := []string{"touch"}
			certainty := toolcontract.NoEffect
			hadEffect := name == "previous operand committed"
			if hadEffect {
				words = append(words, "./allowed.txt")
				certainty = toolcontract.EffectCompleted
			}
			got := h.run("", append(words, "./denied.txt", "./later.txt")...)
			reject := toolrejection.AsToolReject(got.err)
			if reject == nil || reject.Code != "WRITE_SCOPE_DENIED" ||
				reject.Data["path"] != "denied.txt" || reject.Data["profile"] != profile ||
				reject.Data["patterns_list"] != "- `allowed.txt`\n- `later.txt`" {
				t.Fatalf("denial = %+v, error = %v", reject, got.err)
			}
			if got.result.Status != 2 || got.result.Terminal != toolcontract.ProducerRefused ||
				got.result.StopCode != reject.Code || got.result.EffectCertainty != certainty ||
				got.result.Complete || got.result.PublicationEligible {
				t.Fatalf("producer = %+v, want typed refusal with effect certainty %s", got.result, certainty)
			}
			if h.absent("allowed.txt") == hadEffect || !h.absent("denied.txt") || !h.absent("later.txt") {
				t.Fatal("write refusal lost a prior effect or allowed a denied or later operand")
			}
		})
	}
}
