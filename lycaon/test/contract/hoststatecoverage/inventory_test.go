package hoststatecoverage_test

import (
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

// Every current host input reaches each recipient it applies to through a
// generated state whose production Reader delivers it, and every input a
// shipped instruction reads joins to a producer. The report is logged as the
// inventory receipt.
func TestHostStateInventoryHasNoUnexplainedGaps(t *testing.T) {
	t.Cleanup(hoststate.HoldAuthorityMemos())
	ctx := t.Context()
	stock, err := promptrender.LoadStock(ctx)
	if err != nil {
		t.Fatalf("load stock catalog: %v", err)
	}
	domains, err := hoststate.DefaultSeedDomains()
	if err != nil {
		t.Fatalf("seed domains: %v", err)
	}
	coordStates, err := hoststate.CoordinatorStates(ctx)
	if err != nil {
		t.Fatalf("coordinator states: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(ctx)
	if err != nil {
		t.Fatalf("worker states: %v", err)
	}
	profiles, err := sandbox.LoadToolProfilesWithCatalog(stock.Catalog)
	if err != nil {
		t.Fatalf("tool profiles: %v", err)
	}
	names, err := toolvocab.NewCatalog(stock.Schemas, profiles, toolvocab.Provenance{})
	if err != nil {
		t.Fatalf("tool vocabulary: %v", err)
	}
	sites, err := interactions.DiscoverCatalogInstructionsWithGraph(stock.Catalog, stock.Schemas, names, nil)
	if err != nil {
		t.Fatalf("discover instruction sites: %v", err)
	}
	producers, err := joinProducers(sites)
	if err != nil {
		t.Fatalf("join instruction producers: %v", err)
	}

	inv := interactions.NewHostStateInventory(interactions.HostStateInventoryInput{
		Catalog:           stock.Catalog,
		Domains:           domains,
		CoordinatorStates: coordStates,
		WorkerStates:      workerStates,
		Sites:             sites,
		Producers:         producers,
	})
	for _, st := range coordStates {
		if st.Excluded {
			continue
		}
		auth, err := hoststate.FreezeTurnAuthority(ctx, st)
		if err != nil {
			t.Fatalf("coordinator authority %s: %v", st.ID, err)
		}
		inv.ObserveCoordinator(st, auth)
	}
	for _, ws := range workerStates {
		if ws.Excluded {
			continue
		}
		auth, err := hoststate.FreezeWorkerAuthority(ctx, ws)
		if err != nil {
			t.Fatalf("worker authority %s: %v", ws.ID, err)
		}
		inv.ObserveWorker(ws, auth)
	}
	rep, err := inv.Report(ctx)
	if err != nil {
		t.Fatalf("build inventory: %v", err)
	}
	raw, err := rep.MarshalJSONBytes()
	if err != nil {
		t.Fatalf("encode inventory: %v", err)
	}
	t.Logf("host-state inventory JSON:\n%s", raw)
	t.Logf("host-state inventory:\n%s", rep.FormatMarkdown())
	if err := rep.ValidateZeroUnexplainedGaps(); err != nil {
		contractcheck.FailErr(t, "rep.ValidateZeroUnexplainedGaps failed", err)
	}
}

// joinProducers traces site inputs through a producer graph it releases on
// return, so the graph and the frozen authorities never share the heap.
func joinProducers(sites []interactions.InstructionSite) (interactions.ProducerJoin, error) {
	graph, err := interactions.BuildProducerGraph()
	if err != nil {
		return interactions.ProducerJoin{}, err
	}
	defer runtime.GC()
	return interactions.JoinInstructionProducers(sites, graph)
}
