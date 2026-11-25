package hoststatecoverage_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

// domainVerdict is one domain control's evidence.
type domainVerdict struct {
	Control string   `json:"control"`
	Gaps    []string `json:"gaps"`
}

// The approval-posture domain gate compares each consumer with
// gate.AllPostures per recipient: a member one consumer omits, a member
// resolved to another value, and a duplicated or unset domain member each
// fail by name, and the unmodified neighbour passes.
func TestApprovalPostureDomainGateControls(t *testing.T) {
	ctx := t.Context()
	stock, err := promptrender.LoadStock(ctx)
	if err != nil {
		t.Fatalf("load stock catalog: %v", err)
	}
	domains, err := hoststate.DefaultSeedDomains()
	if err != nil {
		t.Fatalf("seed domains: %v", err)
	}
	allCoord, err := hoststate.CoordinatorStates(ctx)
	if err != nil {
		t.Fatalf("coordinator states: %v", err)
	}
	allWorkers, err := hoststate.WorkerStates(ctx)
	if err != nil {
		t.Fatalf("worker states: %v", err)
	}
	var coord []hoststate.CoordinatorState
	var workers []hoststate.WorkerState
	for _, p := range gate.AllPostures() {
		i := slices.IndexFunc(allCoord, func(st hoststate.CoordinatorState) bool { return !st.Excluded && st.ApprovalPosture == p })
		j := slices.IndexFunc(allWorkers, func(ws hoststate.WorkerState) bool { return !ws.Excluded && ws.ApprovalPosture == p })
		if i < 0 || j < 0 {
			t.Fatalf("no delivered state for approval posture %s", p)
		}
		coord, workers = append(coord, allCoord[i]), append(workers, allWorkers[j])
	}

	gaps := func(d *hoststate.SeedDomains, coord []hoststate.CoordinatorState, workers []hoststate.WorkerState) []string {
		t.Helper()
		inv := interactions.NewHostStateInventory(interactions.HostStateInventoryInput{
			Catalog: stock.Catalog, Domains: d, CoordinatorStates: coord, WorkerStates: workers,
		})
		for _, st := range coord {
			auth, err := hoststate.FreezeTurnAuthority(ctx, st)
			if err != nil {
				t.Fatalf("coordinator authority %s: %v", st.ID, err)
			}
			inv.ObserveCoordinator(st, auth)
		}
		for _, ws := range workers {
			auth, err := hoststate.FreezeWorkerAuthority(ctx, ws)
			if err != nil {
				t.Fatalf("worker authority %s: %v", ws.ID, err)
			}
			inv.ObserveWorker(ws, auth)
		}
		rep, err := inv.Report()
		if err != nil {
			t.Fatalf("inventory report: %v", err)
		}
		var out []string
		for _, g := range rep.UnexplainedGaps {
			if g.InputDomain == "approval_posture" {
				out = append(out, g.ValueOrClass+" on "+g.Recipient+": "+g.Detail)
			}
		}
		return out
	}
	without := func(d *hoststate.SeedDomains, p gate.Posture) *hoststate.SeedDomains {
		cp := *d
		cp.ApprovalPostures = slices.DeleteFunc(slices.Clone(d.ApprovalPostures), func(q gate.Posture) bool { return q == p })
		return &cp
	}
	light := func(st hoststate.CoordinatorState) bool { return st.ApprovalPosture == gate.PostureLight }
	lightWorker := func(ws hoststate.WorkerState) bool { return ws.ApprovalPosture == gate.PostureLight }
	// A state that names light but resolved to the balanced default.
	defaulted := slices.Clone(coord)
	for i := range defaulted {
		if light(defaulted[i]) {
			balanced := coord[slices.IndexFunc(coord, func(st hoststate.CoordinatorState) bool { return st.ApprovalPosture == gate.PostureBalanced })]
			balanced.ID = defaulted[i].ID
			defaulted[i] = balanced
		}
	}

	var verdicts []domainVerdict
	for _, c := range []struct {
		name    string
		d       *hoststate.SeedDomains
		coord   []hoststate.CoordinatorState
		workers []hoststate.WorkerState
		want    []string
	}{
		{"unmodified_neighbour", domains, coord, workers, nil},
		{"light_omitted_from_the_coordinator_sweep", domains, slices.DeleteFunc(slices.Clone(coord), light), workers,
			[]string{"light on coordinator: disposition=fuzz_selectable_only deterministic_delivered=false fuzz_selectable=true"}},
		{"light_omitted_from_the_worker_sweep", domains, coord, slices.DeleteFunc(slices.Clone(workers), lightWorker),
			[]string{"light on worker: disposition=fuzz_selectable_only deterministic_delivered=false fuzz_selectable=true"}},
		{"light_dropped_from_the_fuzz_selector", without(domains, gate.PostureLight), coord, workers, []string{
			"light on coordinator: disposition=deterministic_only_missing_fuzz deterministic_delivered=true fuzz_selectable=false",
			"light on worker: disposition=deterministic_only_missing_fuzz deterministic_delivered=true fuzz_selectable=false",
		}},
		{"light_resolved_to_the_balanced_default", domains, defaulted, workers,
			[]string{"light on coordinator: disposition=fuzz_selectable_only deterministic_delivered=false fuzz_selectable=true"}},
	} {
		got := gaps(c.d, c.coord, c.workers)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: approval posture gaps = %q, want %q", c.name, got, c.want)
		}
		verdicts = append(verdicts, domainVerdict{Control: c.name, Gaps: got})
	}

	for _, c := range []struct {
		name    string
		members []gate.Posture
		want    string
	}{
		{"duplicated_member", []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureBalanced, gate.PostureStrict}, "lists balanced twice"},
		{"unset_member", []gate.Posture{gate.PostureLight, "", gate.PostureStrict}, "unset member"},
	} {
		cp := *domains
		cp.ApprovalPostures = c.members
		err := cp.Validate()
		if err == nil || !strings.Contains(err.Error(), "approval postures") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Validate = %v, want an approval postures error naming %q", c.name, err, c.want)
		}
		verdicts = append(verdicts, domainVerdict{Control: c.name, Gaps: []string{errText(err)}})
	}
	raw, _ := json.MarshalIndent(verdicts, "", "  ")
	t.Logf("domain control verdicts:\n%s", raw)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
