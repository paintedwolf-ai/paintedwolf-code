package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCachedProviderListUsesCurrentLocalReadiness(t *testing.T) {
	r := &Registry{}
	r.localRuntime = nativegen.NewRuntime(nativegen.RuntimeConfig{})
	t.Cleanup(func() { _ = r.localRuntime.Close() })
	r.listCache = catalogruntime.NewSnapshotCache(providerListCacheTTL, cloneProviderMetaList)
	cached := []api.ProviderMeta{
		{ID: "local", Kind: "nativegen", Configured: true, ReadyToAssign: true},
		{ID: "external", Kind: "openai", Configured: true, ReadyToAssign: true},
	}
	_, _, _, generation := r.listCache.Read()
	r.listCache.Store(cached, generation)
	first := r.ListCached(t.Context())
	if first[0].LocalRuntime == nil || first[0].LocalRuntime.State != "idle" || first[0].Configured || first[0].ReadyToAssign {
		t.Fatalf("cached local readiness: %+v", first[0])
	}
	if !first[1].Configured || !first[1].ReadyToAssign || first[1].LocalRuntime != nil {
		t.Fatalf("changed external provider: %+v", first[1])
	}
	testutil.FailErr(t, "close local runtime", r.localRuntime.Close())
	if second := r.ListCached(t.Context()); second[0].LocalRuntime.State != "stopped" {
		t.Fatalf("cached process health: %+v", second[0])
	}
	if cached[0].LocalRuntime != nil || !cached[0].Configured {
		t.Fatal("projection changed cached input")
	}
}

func TestLocalRepairRefusesOtherProvidersAndMissingRuntime(t *testing.T) {
	r := &Registry{}
	r.catalog = mustCatalogCloneShipToLocal(t, "providers:\n  - id: local\n    kind: nativegen\n"+MinimalShipHTTPRetryYAML+"  - id: external\n    kind: openai-compatible\n    base_url: http://localhost:1/v1\n"+MinimalShipHTTPRetryYAML)
	for _, id := range []string{"missing", "external", "local"} {
		if err := r.RepairLocalGeneration(t.Context(), id); err == nil {
			t.Fatalf("accepted repair for %s", id)
		}
	}
	if got := r.LocalGenerationStatus(); got.State != "unavailable" || got.RecoveryAction != "reinstall" {
		t.Fatalf("missing runtime status: %+v", got)
	}
}

func TestUnavailableGenerationReportsSavedSelection(t *testing.T) {
	r := &Registry{}
	r.localStore = modelstore.New(t.TempDir(), nil)
	if status := r.LocalGenerationStatus(); status.State != "unavailable" || status.SelectionState != "unconfigured" {
		t.Fatalf("empty store: %+v", status)
	}
	testutil.FailErr(t, "opt out without runtime", r.RemoveLocalModel(t.Context()))
	if status := r.LocalGenerationStatus(); status.State != "unavailable" || status.SelectionState != "opted_out" {
		t.Fatalf("opted out without runtime: %+v", status)
	}

	r.localRuntime = nativegen.NewRuntime(nativegen.RuntimeConfig{ModelStore: r.localStore})
	t.Cleanup(func() { _ = r.localRuntime.Close() })
	out := api.LocalGenerationStatus{State: "unavailable", RecoveryAction: "reinstall"}
	r.localRuntime.SelectionStatus(&out)
	if out.State != "unavailable" || out.SelectionState != "opted_out" {
		t.Fatalf("unavailable runtime: %+v", out)
	}
}
