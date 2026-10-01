package toolhost

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/pkg/api"
)

type runtimeDetectionFixture struct{ matched bool }

func (s runtimeDetectionFixture) MatchAction(hitl.ProposedAction, string) (hitl.DetectionMatch, bool) {
	return hitl.DetectionMatch{PackID: "fixture", RuleID: "rule", Level: "critical",
		External: true, Unrecoverable: true, Tagged: true}, s.matched
}

func TestRuntimeDetectionReloadPreservesSealedGate(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	r, err := NewRuntime(RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(),
		Catalog: extpackstest.StockCatalog(t), Approvals: store})
	testutil.FailErr(t, "build tool runtime", err)
	var published atomic.Pointer[runtimeDetectionFixture]
	r.SetDetectionSource(func() settings.DetectionSource {
		if source := published.Load(); source != nil {
			return source
		}
		return nil
	})
	r.SealApprovalGate()
	sealed := r.gateBuilder.Seal()
	if _, ok := r.approvalGate.PutAskQuiet(hitl.AskQuiet{ChatSessionID: "session", Key: "retained"}, 0); !ok {
		t.Fatal("install ask quiet")
	}
	action := hitl.ProposedAction{Tool: "command", SessionID: "session",
		Args:      map[string]any{"command": "echo hi"},
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy}}
	for _, source := range []settings.DetectionSource{nil, runtimeDetectionFixture{}, runtimeDetectionFixture{true}, nil, runtimeDetectionFixture{}} {
		if source == nil {
			published.Store(nil)
		} else {
			fixture := source.(runtimeDetectionFixture)
			published.Store(&fixture)
		}
		result, err := r.approvalGate.Evaluate(t.Context(), action)
		testutil.FailErr(t, "evaluate reloaded detection source", err)
		if r.gateBuilder.Seal() != sealed {
			t.Fatal("detection reload replaced the sealed gate")
		}
		if _, ok := r.approvalGate.AskQuietLive("session", "retained"); !ok {
			t.Fatal("detection reload discarded session state")
		}
		if source == nil {
			if result == nil || !result.Required() || result.Decision.Primary != api.GateIncompleteFacts {
				t.Fatalf("missing source result = %+v", result)
			}
			continue
		}
		wantAsk := source.(runtimeDetectionFixture).matched
		if (result != nil && result.Required()) != wantAsk {
			t.Fatalf("reloaded source result = %+v, want ask=%v", result, wantAsk)
		}
	}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				published.Store(&runtimeDetectionFixture{})
				_, err := r.approvalGate.Evaluate(t.Context(), action)
				if err != nil {
					t.Errorf("evaluate concurrent reload: %v", err)
				}
			}
		})
	}
	workers.Wait()
}
