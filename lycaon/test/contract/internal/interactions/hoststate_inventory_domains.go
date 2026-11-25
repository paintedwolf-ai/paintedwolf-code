package interactions

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func (inv *inventory) approvalRows() []DomainClassRow {
	const src = "internal/gate/posture.go:AllPostures"
	d := inv.in.Domains
	var rows []DomainClassRow
	for _, p := range gate.AllPostures() {
		c := row("approval_posture", src, string(p), RecipientCoordinator)
		c.FuzzSelectable = coordinatorSelectable(d, func(s *hoststate.CoordinatorSeed) { s.ApprovalPosture = p })
		if st, obs, ok := inv.coordWitness(func(st hoststate.CoordinatorState, obs stateObservation) bool {
			return st.ApprovalPosture == p && obs.ReviewsConfinedWrites == p.ReviewsConfinedWrites()
		}); ok {
			c = c.witness(st.ID, string(st.ApprovalPosture), fmt.Sprintf("reviews_confined_writes=%t", obs.ReviewsConfinedWrites))
		}
		w := row("approval_posture", src, string(p), RecipientWorker)
		w.FuzzSelectable = workerSelectable(d, func(s *hoststate.WorkerSeed) { s.ApprovalPosture = p })
		if ws, obs, ok := inv.workerWitness(func(ws hoststate.WorkerState, obs stateObservation) bool {
			return ws.ApprovalPosture == p && obs.ReviewsConfinedWrites == p.ReviewsConfinedWrites()
		}); ok {
			w = w.witness(ws.ID, string(ws.ApprovalPosture), fmt.Sprintf("reviews_confined_writes=%t", obs.ReviewsConfinedWrites))
		}
		rows = append(rows, c, w)
	}
	return rows
}

// askReadOnly points a worker seed at a read-only agent an Ask parent admits.
func askReadOnly(s *hoststate.WorkerSeed) {
	s.AgentID = "repo-researcher"
	s.BranchView = hoststate.BranchMainGit
}

func (inv *inventory) sessionModeRows() []DomainClassRow {
	const src = "pkg/api/session_mode.generated.go:AllSessionModeValues"
	d := inv.in.Domains
	var rows []DomainClassRow
	for _, m := range api.AllSessionModeValues() {
		c := row("session_mode", src, string(m), RecipientCoordinator)
		c.FuzzSelectable = coordinatorSelectable(d, func(s *hoststate.CoordinatorSeed) { s.SessionMode = m })
		if st, obs, ok := inv.coordWitness(func(st hoststate.CoordinatorState, obs stateObservation) bool {
			return st.SessionMode == m && obs.SessionMode == string(m)
		}); ok {
			c = c.witness(st.ID, obs.SessionMode, "surface="+st.TurnProfile.SurfaceID)
		}
		w := row("session_mode", src, string(m), RecipientWorker)
		w.FuzzSelectable = workerSelectable(d, func(s *hoststate.WorkerSeed) {
			s.ParentSessionMode = m
			if m == api.SessionModeAsk {
				askReadOnly(s)
			}
		})
		if ws, obs, ok := inv.workerWitness(func(ws hoststate.WorkerState, obs stateObservation) bool {
			return ws.ParentSessionMode == m && obs.SessionMode == string(m)
		}); ok {
			w = w.witness(ws.ID, obs.SessionMode, "agent="+ws.AgentID)
		}
		rows = append(rows, c, w)

		for _, up := range []bool{false, true} {
			val := fmt.Sprintf("parent=%s,upgraded=%t", m, up)
			const upSrc = "internal/coordinator/promptloop/round_authority.go:askUpgrade"
			if m != api.SessionModeAsk && up {
				rows = append(rows, impossible("parent_upgraded_ask_to_agent", upSrc, val, RecipientWorker, "only an Ask parent upgrades to Agent"))
				continue
			}
			u := row("parent_upgraded_ask_to_agent", upSrc, val, RecipientWorker)
			u.FuzzSelectable = workerSelectable(d, func(s *hoststate.WorkerSeed) {
				s.ParentSessionMode, s.ParentUpgradedAskToAgent = m, up
				if m == api.SessionModeAsk {
					askReadOnly(s)
				}
			})
			if ws, obs, ok := inv.workerWitness(func(ws hoststate.WorkerState, obs stateObservation) bool {
				return ws.ParentSessionMode == m && ws.ParentUpgradedAskToAgent == up && obs.SessionMode == string(m)
			}); ok {
				u = u.witness(ws.ID, val, "child_session_mode="+obs.SessionMode)
			}
			rows = append(rows, u)
		}
	}
	return rows
}

func (inv *inventory) postureAndOverlayRows() []DomainClassRow {
	d := inv.in.Domains
	var rows []DomainClassRow
	for _, p := range d.Postures {
		const src = "internal/session/profiles:LoadPostureRegistry"
		c := row("session_posture", src, string(p.ID), RecipientCoordinator)
		c.FuzzSelectable = coordinatorSelectable(d, func(s *hoststate.CoordinatorSeed) { s.Posture = p.ID })
		if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, _ stateObservation) bool { return st.Posture == p.ID }); ok {
			c = c.witness(st.ID, string(p.ID), "surface="+st.TurnProfile.SurfaceID)
		}
		w := row("session_posture", src, string(p.ID), RecipientWorker)
		w.FuzzSelectable = workerSelectable(d, func(s *hoststate.WorkerSeed) { s.Posture = p.ID })
		if ws, _, ok := inv.workerWitness(func(ws hoststate.WorkerState, _ stateObservation) bool { return ws.Posture == p.ID }); ok {
			w = w.witness(ws.ID, string(p.ID), "agent="+ws.AgentID)
		}
		rows = append(rows, c, w)
	}
	for ovIdx, ov := range d.Overlays {
		const src = "test/contract/internal/hoststate:DiscoverPackOverlays"
		c := row("pack_overlay", src, ov.ID, RecipientCoordinator)
		c.FuzzSelectable = coordinatorSelectable(d, func(s *hoststate.CoordinatorSeed) { s.OverlayIndex = ovIdx })
		if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, _ stateObservation) bool { return st.PackOverlayID == ov.ID }); ok {
			c = c.witness(st.ID, ov.ID, "surface="+st.TurnProfile.SurfaceID)
		}
		rows = append(rows, c)
		w := row("pack_overlay", src, ov.ID, RecipientWorker)
		if len(ov.WorkerAgents) == 0 {
			rows = append(rows, impossible("pack_overlay", src, ov.ID, RecipientWorker, "overlay leaves no worker agent to spawn"))
			continue
		}
		w.FuzzSelectable = workerSelectable(d, func(s *hoststate.WorkerSeed) {
			s.OverlayIndex = ovIdx
			s.AgentID = ov.WorkerAgents[0].ID
		})
		if ws, _, ok := inv.workerWitness(func(ws hoststate.WorkerState, _ stateObservation) bool { return ws.Overlay.ID == ov.ID }); ok {
			w = w.witness(ws.ID, ov.ID, "agent="+ws.AgentID)
		}
		rows = append(rows, w)
	}
	return rows
}

func (inv *inventory) surfaceAndWorkflowRows() []DomainClassRow {
	d := inv.in.Domains
	var rows []DomainClassRow
	for _, s := range d.Surfaces {
		c := row("coordinator_surface", "internal/coordinator/surfacecatalog:Load", s.ID, RecipientCoordinator)
		c.FuzzSelectable = coordinatorSelectable(d, func(seed *hoststate.CoordinatorSeed) {
			seed.WorkflowState, seed.SurfaceID = hoststate.WorkflowActivePhase, s.ID
		})
		if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, _ stateObservation) bool {
			return st.TurnProfile.SurfaceID == s.ID
		}); ok {
			c = c.witness(st.ID, s.ID, "surface="+st.TurnProfile.SurfaceID)
		}
		rows = append(rows, c)
	}
	rows = append(rows, impossible("coordinator_surface", "internal/coordinator/promptloop/worker_tool_plan.go:surfaceAllowsTool",
		"any_coordinator_surface", RecipientWorker, "workers have no coordinator surface compile"))
	for _, wf := range d.WorkflowStates {
		c := row("workflow_state", "internal/coordinator/surface:ResolveTurnProfile", string(wf), RecipientCoordinator)
		c.FuzzSelectable = coordinatorSelectable(d, func(s *hoststate.CoordinatorSeed) { s.WorkflowState = wf })
		if st, _, ok := inv.coordWitness(func(st hoststate.CoordinatorState, _ stateObservation) bool { return st.WorkflowState == wf }); ok {
			c = c.witness(st.ID, string(wf), "surface="+st.TurnProfile.SurfaceID)
		}
		rows = append(rows, c)
	}
	return rows
}

func (inv *inventory) rootAndBranchRows() []DomainClassRow {
	const src = "internal/session/readerfacts/resolver.go:gitFacts"
	d := inv.in.Domains
	var rows []DomainClassRow
	for _, pGit := range []bool{false, true} {
		for _, aGit := range []bool{false, true} {
			cls := fmt.Sprintf("multi_root:primary_git=%t,any_git=%t", pGit, aGit)
			if pGit && !aGit {
				for _, rec := range []string{RecipientCoordinator, RecipientWorker} {
					rows = append(rows, impossible("workspace_roots_multi_git", src, cls, rec, "the any-root probe includes the primary root"))
				}
				continue
			}
			match := func(obs stateObservation) bool {
				return obs.MultiRoot && obs.HasGit == pGit && obs.HasAnyGitRoot == aGit
			}
			c := row("workspace_roots_multi_git", src, cls, RecipientCoordinator)
			for _, rt := range d.RootTopologies {
				c.FuzzSelectable = c.FuzzSelectable || coordinatorResolvesTo(d, func(s *hoststate.CoordinatorSeed) { s.RootTopology = rt },
					func(st hoststate.CoordinatorState) bool {
						return st.RootCount >= 2 && st.HasGit == pGit && st.HasAnyGitRoot == aGit
					})
			}
			if st, obs, ok := inv.coordWitness(func(_ hoststate.CoordinatorState, obs stateObservation) bool { return match(obs) }); ok {
				c = c.witness(st.ID, string(st.RootTopology), fmt.Sprintf("has_git=%t has_any_git_root=%t", obs.HasGit, obs.HasAnyGitRoot))
			}
			w := row("workspace_roots_multi_git", src, cls, RecipientWorker)
			for _, bv := range d.WorkerBranches {
				for _, sec := range []bool{false, true} {
					w.FuzzSelectable = w.FuzzSelectable || workerResolvesTo(d, func(s *hoststate.WorkerSeed) {
						s.BranchView, s.RootCount, s.SecondaryRootGit = bv, 2, sec
					}, func(ws hoststate.WorkerState) bool { return ws.HasGit == pGit && ws.HasAnyGitRoot == aGit })
				}
			}
			if ws, obs, ok := inv.workerWitness(func(_ hoststate.WorkerState, obs stateObservation) bool { return match(obs) }); ok {
				w = w.witness(ws.ID, fmt.Sprintf("branch=%s secondary_git=%t", ws.BranchView, ws.SecondaryRootGit),
					fmt.Sprintf("has_git=%t has_any_git_root=%t", obs.HasGit, obs.HasAnyGitRoot))
			}
			rows = append(rows, c, w)
		}
	}
	for _, bv := range d.WorkerBranches {
		b := row("worker_branch_view", "internal/session/workerworkspace:OnWorkerBranch", string(bv), RecipientWorker)
		b.FuzzSelectable = workerSelectable(d, func(s *hoststate.WorkerSeed) { s.BranchView = bv })
		if ws, obs, ok := inv.workerWitness(func(ws hoststate.WorkerState, _ stateObservation) bool { return ws.BranchView == bv }); ok {
			b = b.witness(ws.ID, string(bv), fmt.Sprintf("on_worker_branch=%t has_git=%t", ws.OnWorkerBranch, obs.HasGit))
		}
		rows = append(rows, b)
	}
	return rows
}
