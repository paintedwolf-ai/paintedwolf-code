package interactions

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func (inv *HostStateInventory) loadingAndAgentRows(ctx context.Context) []DomainClassRow {
	d := inv.in.Domains
	var rows []DomainClassRow
	partly := func(obs stateObservation) bool { return len(obs.Activated) > 0 && len(obs.Remaining) > 0 }
	for _, ld := range d.LoadingModes {
		c := row("deferred_tool_loading", "internal/coordinator/promptloop:compileRuntimeToolPlan", string(ld), RecipientCoordinator)
		c.FuzzSelectable = coordinatorResolvesTo(d, func(s *hoststate.CoordinatorSeed) { s.Loading = ld }, func(st hoststate.CoordinatorState) bool { return st.Loading == ld })
		if st, obs, ok := inv.coordWitness(func(st hoststate.CoordinatorState, obs stateObservation) bool {
			return st.Loading == ld && (ld != hoststate.LoadingPartlyLoaded || partly(obs))
		}); ok {
			c = c.witness(st.ID, string(ld), fmt.Sprintf("activated=%v remaining=%v", obs.Activated, obs.Remaining))
		}
		rows = append(rows, c)
	}
	for _, ld := range d.WorkerLoadings {
		w := row("deferred_tool_loading", "internal/coordinator/promptloop/worker_tool_plan.go:compileWorkerToolPlan", string(ld), RecipientWorker)
		w.FuzzSelectable = workerResolvesTo(ctx, d, func(s *hoststate.WorkerSeed) { s.Loading = ld }, func(ws hoststate.WorkerState) bool { return ws.Loading == ld })
		if ws, obs, ok := inv.workerWitness(func(ws hoststate.WorkerState, obs stateObservation) bool {
			return ws.Loading == ld && (ld != hoststate.WorkerLoadingPartlyLoaded || partly(obs))
		}); ok {
			w = w.witness(ws.ID, string(ld), fmt.Sprintf("activated=%v remaining=%v", obs.Activated, obs.Remaining))
		}
		rows = append(rows, w)
	}
	if len(d.Overlays) == 0 {
		return rows
	}
	for _, ag := range d.Overlays[0].WorkerAgents {
		a := row("worker_agent_profile", "internal/agentdef:LoadEffectiveWithCatalog", ag.ID+":"+ag.ToolProfile, RecipientWorker)
		a.FuzzSelectable = workerResolvesTo(ctx, d, func(s *hoststate.WorkerSeed) { s.AgentID = ag.ID }, func(ws hoststate.WorkerState) bool { return ws.AgentID == ag.ID })
		if ws, _, ok := inv.workerWitness(func(ws hoststate.WorkerState, _ stateObservation) bool { return ws.AgentID == ag.ID }); ok {
			a = a.witness(ws.ID, ag.ID, "profile="+ws.ToolProfile)
		}
		rows = append(rows, a)
	}
	return rows
}

// envToggle is one Boolean host input with its seed setters and the delivered
// observation that carries it.
type envToggle struct {
	domain, source string
	coord          func(*hoststate.CoordinatorSeed, bool)
	worker         func(*hoststate.WorkerSeed, bool)
	coordState     func(hoststate.CoordinatorState) bool
	workerState    func(hoststate.WorkerState) bool
	delivered      func(stateObservation) bool
}

var envToggles = []envToggle{
	{
		domain: "web_search", source: "internal/coordinator/promptloop:webSearchEnabled",
		coord:       func(s *hoststate.CoordinatorSeed, b bool) { s.WebSearch = b },
		worker:      func(s *hoststate.WorkerSeed, b bool) { s.WebSearch = b },
		coordState:  func(st hoststate.CoordinatorState) bool { return st.WebSearch },
		workerState: func(ws hoststate.WorkerState) bool { return ws.WebSearch },
		delivered:   func(o stateObservation) bool { return o.WebSearch },
	},
	{
		domain: "vision", source: "internal/prompts:Reader.WithVision",
		coord:       func(s *hoststate.CoordinatorSeed, b bool) { s.Vision = b },
		worker:      func(s *hoststate.WorkerSeed, b bool) { s.Vision = b },
		coordState:  func(st hoststate.CoordinatorState) bool { return st.Vision },
		workerState: func(ws hoststate.WorkerState) bool { return ws.Vision },
		delivered:   func(o stateObservation) bool { return o.Vision },
	},
	{
		domain: "command_external_hidden", source: "internal/session/profiles:CommandExternalHidden",
		coord:       func(s *hoststate.CoordinatorSeed, b bool) { s.CommandExternalHidden = b },
		worker:      func(s *hoststate.WorkerSeed, b bool) { s.CommandExternalHidden = b },
		coordState:  func(st hoststate.CoordinatorState) bool { return st.CommandExternalHidden },
		workerState: func(ws hoststate.WorkerState) bool { return ws.CommandExternalHidden },
		delivered:   func(o stateObservation) bool { return o.ExternalHidden },
	},
}

func (inv *HostStateInventory) envToggleRows(ctx context.Context) []DomainClassRow {
	d := inv.in.Domains
	var rows []DomainClassRow
	for _, tg := range envToggles {
		for _, b := range []bool{false, true} {
			val := fmt.Sprintf("%t", b)
			c := row(tg.domain, tg.source, val, RecipientCoordinator)
			c.FuzzSelectable = coordinatorResolvesTo(d, func(s *hoststate.CoordinatorSeed) { tg.coord(s, b) }, func(st hoststate.CoordinatorState) bool { return tg.coordState(st) == b })
			if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, obs stateObservation) bool {
				return tg.coordState(st) == b && tg.delivered(obs) == b
			}); ok {
				c = c.witness(st.ID, val, "reader="+val)
			}
			w := row(tg.domain, tg.source, val, RecipientWorker)
			w.FuzzSelectable = workerResolvesTo(ctx, d, func(s *hoststate.WorkerSeed) { tg.worker(s, b) }, func(ws hoststate.WorkerState) bool { return tg.workerState(ws) == b })
			if ws, _, ok := inv.workerWitness(func(ws hoststate.WorkerState, obs stateObservation) bool {
				return tg.workerState(ws) == b && tg.delivered(obs) == b
			}); ok {
				w = w.witness(ws.ID, val, "agent="+ws.AgentID+" reader="+val)
			}
			rows = append(rows, c, w)
		}
	}
	for _, tmp := range d.TempStates {
		c := row("temp_roots", "internal/session/profiles:WritableSharedTemp", string(tmp), RecipientCoordinator)
		c.FuzzSelectable = coordinatorResolvesTo(d, func(s *hoststate.CoordinatorSeed) { s.TempState = tmp }, func(st hoststate.CoordinatorState) bool { return st.TempState == tmp })
		if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, _ stateObservation) bool { return st.TempState == tmp }); ok {
			c = c.witness(st.ID, string(tmp), fmt.Sprintf("scratch=%t shared=%d", st.SessionScratchDir != "", len(st.SharedTempLocations)))
		}
		rows = append(rows, c)
	}
	for _, tmp := range d.TempStates {
		w := row("temp_roots", "internal/session/profiles:WritableSharedTemp", string(tmp), RecipientWorker)
		w.FuzzSelectable = workerResolvesTo(ctx, d, func(s *hoststate.WorkerSeed) { s.TempState = tmp }, func(ws hoststate.WorkerState) bool { return ws.TempState == tmp })
		if ws, _, ok := inv.workerWitness(func(ws hoststate.WorkerState, _ stateObservation) bool { return ws.TempState == tmp }); ok {
			w = w.witness(ws.ID, string(tmp), fmt.Sprintf("scratch=%t shared=%d", ws.SessionScratchDir != "", len(ws.SharedTempLocations)))
		}
		rows = append(rows, w)
	}
	for _, mcp := range d.MCPStates {
		c := row("mcp_attachment", "internal/tools:PlanMCPTools", string(mcp), RecipientCoordinator)
		c.FuzzSelectable = coordinatorResolvesTo(d, func(s *hoststate.CoordinatorSeed) { s.MCPState = mcp }, func(st hoststate.CoordinatorState) bool { return st.MCPState == mcp })
		if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, _ stateObservation) bool { return st.MCPState == mcp }); ok {
			c = c.witness(st.ID, string(mcp), "mcp="+string(st.MCPState))
		}
		rows = append(rows, c)
	}
	return rows
}

// Fuzz bases: each varies one coordinate from a seed production admits.
func coordinatorBases(d *hoststate.SeedDomains) []hoststate.CoordinatorSeed {
	var out []hoststate.CoordinatorSeed
	for _, wf := range []hoststate.WorkflowState{hoststate.WorkflowAmbientIdle, hoststate.WorkflowActivePhase} {
		for _, p := range d.Postures {
			for _, ld := range []hoststate.LoadingMode{hoststate.LoadingFloorOnly, hoststate.LoadingAllLoaded} {
				for _, s := range d.Surfaces {
					out = append(out, hoststate.CoordinatorSeed{
						SessionMode: api.SessionModeAgent, Posture: p.ID, WorkflowState: wf, SurfaceID: s.ID,
						RootTopology: hoststate.RootsSingleGit, Loading: ld, WebSearch: true,
						ApprovalPosture: gate.PostureBalanced, TempState: hoststate.TempScratchOnly, MCPState: hoststate.MCPNone,
					})
					if wf == hoststate.WorkflowAmbientIdle {
						break
					}
				}
			}
		}
	}
	return out
}

func workerBases(d *hoststate.SeedDomains, overlay int) []hoststate.WorkerSeed {
	var out []hoststate.WorkerSeed
	if overlay >= len(d.Overlays) {
		return nil
	}
	for _, ag := range d.Overlays[overlay].WorkerAgents {
		for _, ld := range []hoststate.WorkerLoadingMode{hoststate.WorkerLoadingStickyOnly, hoststate.WorkerLoadingAllLoaded} {
			out = append(out, hoststate.WorkerSeed{
				OverlayIndex: overlay, AgentID: ag.ID, Posture: api.SessionPostureBuild, BranchView: hoststate.BranchMainGit,
				RootCount: 1, Loading: ld, ParentSessionMode: api.SessionModeAgent, WebSearch: true, ApprovalPosture: gate.PostureBalanced,
				TempState: hoststate.TempNone,
			})
		}
	}
	return out
}

// coordinatorResolvesTo reports whether a mutated coordinator seed survives the
// fuzz codec round trip and resolves to an admitted state accept takes.
func coordinatorResolvesTo(d *hoststate.SeedDomains, mutate func(*hoststate.CoordinatorSeed), accept func(hoststate.CoordinatorState) bool) bool {
	for _, seed := range coordinatorBases(d) {
		mutate(&seed)
		raw, err := d.EncodeCoordinator(seed, hoststate.SeedTail{})
		if err != nil {
			continue
		}
		decoded, err := d.Decode(raw)
		if err != nil || decoded.Worker || decoded.Coordinator != seed {
			continue
		}
		st, err := hoststate.ResolveCoordinatorSeed(d, decoded.Coordinator)
		if err == nil && !st.Excluded && accept(st) {
			return true
		}
	}
	return false
}

// workerResolvesTo is coordinatorResolvesTo for worker seeds, starting from
// every agent of the overlay a mutation selects.
func workerResolvesTo(ctx context.Context, d *hoststate.SeedDomains, mutate func(*hoststate.WorkerSeed), accept func(hoststate.WorkerState) bool) bool {
	probe := hoststate.WorkerSeed{}
	mutate(&probe)
	for _, seed := range workerBases(d, probe.OverlayIndex) {
		mutate(&seed)
		raw, err := d.EncodeWorker(seed, hoststate.SeedTail{})
		if err != nil {
			continue
		}
		decoded, err := d.Decode(raw)
		if err != nil || !decoded.Worker || !decoded.WorkerRoute || decoded.WorkerSeed != seed {
			continue
		}
		ws, err := hoststate.ResolveWorkerSeed(ctx, d, decoded.WorkerSeed)
		if err == nil && !ws.Excluded && accept(ws) {
			return true
		}
	}
	return false
}
