package promptrender_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

// Every current host input reaches each recipient it applies to through a
// generated state whose production Reader delivers it, and every input a
// shipped instruction reads joins to a producer. The report is logged as the
// inventory receipt.
func TestHostStateInventoryHasNoUnexplainedGaps(t *testing.T) {
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
	sum, err := promptrender.RunCartesianMatrix(ctx, stock)
	if err != nil {
		t.Fatalf("host-state matrix: %v", err)
	}
	profiles, err := sandbox.LoadToolProfilesWithCatalog(stock.Catalog)
	if err != nil {
		t.Fatalf("tool profiles: %v", err)
	}
	names, err := toolvocab.NewCatalog(stock.Schemas, profiles, toolvocab.Provenance{})
	if err != nil {
		t.Fatalf("tool vocabulary: %v", err)
	}
	graph, err := interactions.DefaultProducerGraph()
	if err != nil {
		t.Fatalf("producer graph: %v", err)
	}
	sites, err := interactions.DiscoverCatalogInstructionsWithGraph(stock.Catalog, stock.Schemas, names, graph)
	if err != nil {
		t.Fatalf("discover instruction sites: %v", err)
	}

	coordAuth := make(map[string]hoststate.TurnAuthority, len(coordStates))
	for _, st := range coordStates {
		if st.Excluded {
			continue
		}
		if coordAuth[st.ID], err = hoststate.FreezeTurnAuthority(ctx, st); err != nil {
			t.Fatalf("coordinator authority %s: %v", st.ID, err)
		}
	}
	workerAuth := make(map[string]hoststate.TurnAuthority, len(workerStates))
	for _, ws := range workerStates {
		if ws.Excluded {
			continue
		}
		if workerAuth[ws.ID], err = hoststate.FreezeWorkerAuthority(ctx, ws); err != nil {
			t.Fatalf("worker authority %s: %v", ws.ID, err)
		}
	}

	rep, err := interactions.BuildHostStateInventory(interactions.HostStateInventoryInput{
		Catalog:                stock.Catalog,
		Domains:                domains,
		CoordinatorStates:      coordStates,
		WorkerStates:           workerStates,
		CoordinatorAuthorities: coordAuth,
		WorkerAuthorities:      workerAuth,
		Coverage:               &sum.Coverage,
		Sites:                  sites,
		Graph:                  graph,
	})
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
		t.Fatal(err)
	}
}
