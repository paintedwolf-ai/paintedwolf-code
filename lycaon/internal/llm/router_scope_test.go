package llm

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRouterRebindingPreservesRoundRobinPerScope(t *testing.T) {
	router := testRouter(t)
	first, second := t.TempDir(), t.TempDir()
	for round := range 6 {
		want := fakeModelPolicy.AgentPool.Models[round%2]
		for _, root := range []string{"", first, second} {
			bound := router.WithScope(SettingsScopeGlobal, "")
			if root != "" {
				if round%2 == 0 {
					bound = router.WithScope(SettingsScopeProject, root)
				} else {
					bound = router.WithOverlayRoots([]string{root})
				}
			}
			got, err := bound.Select(t.Context())
			testutil.FailErr(t, "select scoped worker", err)
			if got.ProviderID != want.ProviderID || got.Model != want.Model {
				t.Fatalf("round %d scope %q: got %+v, want %+v", round, root, got, want)
			}
		}
	}
}

func TestConcurrentRouterBindingsDistributePoolEvenly(t *testing.T) {
	router := testRouter(t)
	root := t.TempDir()
	const count = 40
	selections := make(chan string, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			got, err := router.WithOverlayRoots([]string{root}).Select(t.Context())
			if err != nil {
				t.Errorf("select concurrent worker: %v", err)
				return
			}
			selections <- got.Model
		})
	}
	workers.Wait()
	close(selections)
	counts := make(map[string]int)
	for model := range selections {
		counts[model]++
	}
	for _, ref := range fakeModelPolicy.AgentPool.Models {
		if counts[ref.Model] != count/2 {
			t.Errorf("model %s selected %d times, want %d", ref.Model, counts[ref.Model], count/2)
		}
	}
}

func TestRouterEquivalentRootsShareCursor(t *testing.T) {
	router := testRouter(t)
	root := t.TempDir()
	for i, roots := range [][]string{{root}, {root + "/."}, {root, root + "/."}} {
		got, err := router.WithOverlayRoots(roots).Select(t.Context())
		testutil.FailErr(t, "select equivalent roots", err)
		if got.Model != fakeModelPolicy.AgentPool.Models[i%2].Model {
			t.Fatalf("roots %v restarted the cursor: %+v", roots, got)
		}
	}
}

func TestRouterPolicyRefreshAndCanceledSelection(t *testing.T) {
	router := testRouter(t)
	router.policy.globalPath = filepath.Join(t.TempDir(), "model-policy.yaml")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := router.WithOverlayRoots(nil).Select(ctx); err == nil {
		t.Fatal("canceled selection succeeded")
	}
	got, err := router.WithOverlayRoots(nil).Select(t.Context())
	testutil.FailErr(t, "select after cancellation", err)
	if got.Model != fakeModelPolicy.AgentPool.Models[0].Model {
		t.Fatalf("cancellation advanced cursor: %+v", got)
	}
	policy := fakeModelPolicy
	policy.AgentPool.Models = []ModelRef{{ProviderID: "new-provider", Model: "new-model"}}
	testutil.FailErr(t, "replace global pool", router.policy.PutGlobal(policy))
	got, err = router.WithScope(SettingsScopeGlobal, "").Select(t.Context())
	testutil.FailErr(t, "select refreshed policy", err)
	if got.Model != "new-model" || got.ProviderID != "new-provider" {
		t.Fatalf("stale pool after policy replacement: %+v", got)
	}
}
