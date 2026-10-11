package workflow

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const topologyHostHoldKind = "topology"

// HostObligationHold parks coordinator turns until host-observed work settles.
type HostObligationHold struct {
	Kind string
}

// HostObligationHolds returns the active run's unsettled host obligations.
// Callers leave turns enabled on read errors so the session can report failures.
func (m *Obligations) HostObligationHolds(ctx context.Context, sessionID string) ([]HostObligationHold, error) {
	if m == nil || m.Runs == nil {
		return nil, nil
	}
	run, err := m.Runs.ActiveBySession(ctx, strings.TrimSpace(sessionID))
	if err != nil || run == nil {
		return nil, err
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return nil, nil
	}
	if !def.MayHostHold() {
		return nil, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	holds := make([]HostObligationHold, 0, len(def.OnEnter.Obligations)+1)
	if def.TopologyBound() {
		gateMet, _, gateErr := m.Gates.PhaseGateMet(ctx, manifest, run, vars)
		if gateErr != nil {
			return nil, gateErr
		}
		if !gateMet {
			holds = append(holds, HostObligationHold{Kind: topologyHostHoldKind})
		}
	}
	for _, ob := range def.OnEnter.Obligations {
		leaf := workflowdef.ObligationGateLeaf(ob.Kind)
		// A kind the phase does not gate on cannot hold the phase.
		if !workflowdef.PhaseHasGate(def, leaf) {
			continue
		}
		kind := m.Kinds[ob.Kind]
		if kind == nil {
			// Phase entry reports unregistered kinds; parking would hide the error.
			continue
		}
		status, err := kind.Status(ctx, run.ID, def.ID)
		if err != nil {
			return nil, err
		}
		if conditions.ObligationSettled(status, vars, ob.Kind) {
			continue
		}
		holds = append(holds, HostObligationHold{Kind: ob.Kind})
	}
	sort.Slice(holds, func(i, j int) bool { return holds[i].Kind < holds[j].Kind })
	if len(holds) == 0 {
		return nil, nil
	}
	return holds, nil
}

// HostObligationHeld reports whether a host observer holds the current phase.
func (m *Obligations) HostObligationHeld(ctx context.Context, sessionID string) (bool, error) {
	holds, err := m.HostObligationHolds(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return len(holds) > 0, nil
}

// HostObligationHoldKinds names the holding kinds for park reasons and copy.
func (m *Obligations) HostObligationHoldKinds(ctx context.Context, sessionID string) []string {
	holds, err := m.HostObligationHolds(ctx, sessionID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(holds))
	for _, hold := range holds {
		out = append(out, hold.Kind)
	}
	return out
}
