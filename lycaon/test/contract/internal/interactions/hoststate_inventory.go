package interactions

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// DeliveredStateObservation records the independently resolved input and the
// recipient-delivered projection or branch outcome for one coordinator or worker state.
type DeliveredStateObservation struct {
	StateID                string
	Recipient              string
	ResolvedApprovalToken  string
	ResolvedSessionMode    string
	ReviewsConfinedWrites  bool
	PrimaryRootGit         bool
	AnyRootGit             bool
	MultiRoot              bool
	WebSearchEnabled       bool
	VisionEnabled          bool
	CommandExternalHidden  bool
	ActivatedDeferredTools []string
	RemainingDeferredTools []string
	RenderedMarkers        []string
}

// DomainClassRow records coverage of one production input value or equivalence
// class on one applicable recipient, keeping raw enum selection separate from
// downstream Boolean branch reachability.
type DomainClassRow struct {
	InputDomain            string `json:"input_domain"`
	ProductionSource       string `json:"production_source"`
	ValueOrClass           string `json:"value_or_class"`
	Recipient              string `json:"recipient"`
	Disposition            string `json:"disposition"`
	DeterministicDelivered bool   `json:"deterministic_delivered"`
	FuzzSelectable         bool   `json:"fuzz_selectable"`
	ProductionImpossible   bool   `json:"production_impossible,omitempty"`
	ImpossibilityReason    string `json:"impossibility_reason,omitempty"`
	WitnessStateID         string `json:"witness_state_id,omitempty"`
	ExclusionWitnessID     string `json:"exclusion_witness_id,omitempty"`
	ResolvedInputValue     string `json:"resolved_input_value,omitempty"`
	GuidanceRelevantResult string `json:"guidance_relevant_result,omitempty"`
}

// ReaderFactInventoryRow records observed values for a declared reader fact by recipient.
type ReaderFactInventoryRow struct {
	FactName         string   `json:"fact_name"`
	FactType         string   `json:"fact_type"`
	Recipient        string   `json:"recipient"`
	ObservedClasses  []string `json:"observed_classes"`
	WitnessStateIDs  []string `json:"witness_state_ids"`
	ConstraintReason string   `json:"constraint_reason,omitempty"`
}

// BoundaryPartitionRow records boundary classes generated for an anchor envelope variable.
type BoundaryPartitionRow struct {
	AnchorID          string   `json:"anchor_id"`
	VarName           string   `json:"var_name"`
	VarType           string   `json:"var_type"`
	ShippedPredicates []string `json:"shipped_predicates,omitempty"`
	GeneratedClasses  []string `json:"generated_classes"`
}

// InventoryGap identifies one unexplained omission or unmatched producer in the state inventory.
type InventoryGap struct {
	Category         string `json:"category"`
	InputDomain      string `json:"input_domain"`
	ValueOrClass     string `json:"value_or_class"`
	Recipient        string `json:"recipient"`
	ProductionSource string `json:"production_source"`
	Detail           string `json:"detail"`
}

// HostStateInventoryInput supplies candidate sources and actual observations for inventory generation.
type HostStateInventoryInput struct {
	SourceCommit             string
	SourceTree               string
	ConstructorSetLabel      string
	EffectiveCatalog         *extpacks.EffectiveCatalog
	Domains                  *hoststate.SeedDomains
	CoordinatorStates        []hoststate.CoordinatorState
	WorkerStates             []hoststate.WorkerState
	CoordinatorAuthorities   map[string]hoststate.TurnAuthority
	WorkerAuthorities        map[string]hoststate.TurnAuthority
	DeliveredObservations    map[string]DeliveredStateObservation
	WiringApprovalTokens     map[string]bool
	WiringRootGitPairs       map[string]bool
	WiringWorkerPartialTools []string
	Coverage                 *hoststate.CoverageReport
	Ledger                   *InteractionLedger
}

// HostStateInventoryReport summarizes current authority domains, reader facts,
// envelope boundary partitions, producer joins, and unexplained gaps.
type HostStateInventoryReport struct {
	SourceCommit            string                   `json:"source_commit"`
	SourceTree              string                   `json:"source_tree"`
	ConstructorSetLabel     string                   `json:"constructor_set_label"`
	CatalogRevision         string                   `json:"catalog_revision"`
	TotalCoordinatorStates  int                      `json:"total_coordinator_states"`
	TotalWorkerStates       int                      `json:"total_worker_states"`
	TotalInstructionSites   int                      `json:"total_instruction_sites"`
	TotalAnchorSpecs        int                      `json:"total_anchor_specs"`
	DomainClasses           []DomainClassRow         `json:"domain_classes"`
	ReaderFactCoverage      []ReaderFactInventoryRow `json:"reader_fact_coverage"`
	BoundaryPartitions      []BoundaryPartitionRow   `json:"boundary_partitions"`
	MatchedProducerInputs   []string                 `json:"matched_producer_inputs"`
	UnmatchedProducers      []string                 `json:"unmatched_producers,omitempty"`
	UnexplainedGaps         []InventoryGap           `json:"unexplained_gaps,omitempty"`
}

// BuildHostStateInventory derives the host-state coverage report from current
// production registries, resolved states, delivered observations, and ledger sites.
func BuildHostStateInventory(in HostStateInventoryInput) (*HostStateInventoryReport, error) {
	if in.Domains == nil || in.EffectiveCatalog == nil {
		return nil, fmt.Errorf("domains and effective catalog required")
	}
	rows, err := buildAuthorityDomainRows(in)
	if err != nil {
		return nil, err
	}
	factRows, err := buildReaderFactRows(in)
	if err != nil {
		return nil, err
	}
	boundaries, err := buildBoundaryPartitions(in.Ledger)
	if err != nil {
		return nil, err
	}
	matched, unmatched, err := reconcileInstructionProducers(in)
	if err != nil {
		return nil, err
	}

	rep := &HostStateInventoryReport{
		SourceCommit:           in.SourceCommit,
		SourceTree:             in.SourceTree,
		ConstructorSetLabel:    in.ConstructorSetLabel,
		CatalogRevision:        in.EffectiveCatalog.Revision,
		TotalCoordinatorStates: len(in.CoordinatorStates),
		TotalWorkerStates:      len(in.WorkerStates),
		DomainClasses:          rows,
		ReaderFactCoverage:     factRows,
		BoundaryPartitions:     boundaries,
		MatchedProducerInputs:  matched,
		UnmatchedProducers:     unmatched,
	}
	if in.Ledger != nil {
		rep.TotalInstructionSites = in.Ledger.TotalDiscoveredSites
	}
	if specs, sErr := hoststate.AnchorCatalogSpecs(); sErr == nil {
		rep.TotalAnchorSpecs = len(specs)
	}
	rep.UnexplainedGaps = computeInventoryGaps(rep, in.Coverage)
	return rep, nil
}

func buildAuthorityDomainRows(in HostStateInventoryInput) ([]DomainClassRow, error) {
	var rows []DomainClassRow
	rows = append(rows, buildApprovalRows(in)...)
	rows = append(rows, buildSessionModeRows(in)...)
	rows = append(rows, buildPostureAndOverlayRows(in)...)
	rows = append(rows, buildSurfaceAndWorkflowRows(in)...)
	rows = append(rows, buildRootAndBranchRows(in)...)
	rows = append(rows, buildLoadingAndAgentRows(in)...)
	rows = append(rows, buildEnvToggleRows(in)...)
	slices.SortFunc(rows, func(a, b DomainClassRow) int {
		return strings.Compare(
			a.InputDomain+"|"+a.Recipient+"|"+a.ValueOrClass,
			b.InputDomain+"|"+b.Recipient+"|"+b.ValueOrClass,
		)
	})
	return rows, nil
}

func finalizeRow(r DomainClassRow) DomainClassRow {
	switch {
	case r.ProductionImpossible:
		r.Disposition = "production_impossible"
	case r.DeterministicDelivered && r.FuzzSelectable:
		r.Disposition = "deterministic_delivered"
	case !r.DeterministicDelivered && r.FuzzSelectable:
		r.Disposition = "fuzz_selectable_only"
	case r.DeterministicDelivered && !r.FuzzSelectable:
		r.Disposition = "deterministic_only_missing_fuzz"
	default:
		r.Disposition = "missing"
	}
	return r
}

func buildApprovalRows(in HostStateInventoryInput) []DomainClassRow {
	const src = "internal/gate/posture.go:AllPostures"
	var rows []DomainClassRow
	for _, p := range gate.AllPostures() {
		fuzzHas := slices.Contains(in.Domains.ApprovalPostures, p)
		var cRow DomainClassRow
		cRow.InputDomain, cRow.ProductionSource, cRow.ValueOrClass, cRow.Recipient = "approval_posture", src, string(p), "coordinator"
		cRow.FuzzSelectable = fuzzHas && coordFuzzResolves(in.Domains, func(s *hoststate.CoordinatorSeed) { s.ApprovalPosture = p })
		for _, st := range in.CoordinatorStates {
			if st.Excluded || st.ApprovalPosture != p {
				continue
			}
			obs, ok := in.DeliveredObservations[st.ID]
			if ok && obs.ResolvedApprovalToken == string(p) && obs.ReviewsConfinedWrites == p.ReviewsConfinedWrites() {
				cRow.DeterministicDelivered = true
				cRow.WitnessStateID = st.ID
				cRow.ResolvedInputValue = obs.ResolvedApprovalToken
				cRow.GuidanceRelevantResult = fmt.Sprintf("reviews_confined_writes=%t markers=%v wiring_token=%t",
					obs.ReviewsConfinedWrites, obs.RenderedMarkers, in.WiringApprovalTokens["coordinator:"+string(p)])
				break
			}
		}
		rows = append(rows, finalizeRow(cRow))

		var wRow DomainClassRow
		wRow.InputDomain, wRow.ProductionSource, wRow.ValueOrClass, wRow.Recipient = "approval_posture", src, string(p), "worker"
		wRow.FuzzSelectable = fuzzHas && workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) { s.ApprovalPosture = p })
		for _, ws := range in.WorkerStates {
			if ws.Excluded || ws.ApprovalPosture != p {
				continue
			}
			obs, ok := in.DeliveredObservations[ws.ID]
			if ok && obs.ResolvedApprovalToken == string(p) && obs.ReviewsConfinedWrites == p.ReviewsConfinedWrites() {
				wRow.DeterministicDelivered = true
				wRow.WitnessStateID = ws.ID
				wRow.ResolvedInputValue = obs.ResolvedApprovalToken
				wRow.GuidanceRelevantResult = fmt.Sprintf("reviews_confined_writes=%t markers=%v wiring_token=%t",
					obs.ReviewsConfinedWrites, obs.RenderedMarkers, in.WiringApprovalTokens["worker:"+string(p)])
				break
			}
		}
		rows = append(rows, finalizeRow(wRow))
	}
	return rows
}

func buildSessionModeRows(in HostStateInventoryInput) []DomainClassRow {
	const src = "pkg/api/session_mode.generated.go:AllSessionModeValues"
	var rows []DomainClassRow
	for _, m := range api.AllSessionModeValues() {
		fuzzHas := slices.Contains(in.Domains.SessionModes, m)
		cRow := DomainClassRow{
			InputDomain: "session_mode", ProductionSource: src, ValueOrClass: string(m), Recipient: "coordinator",
			FuzzSelectable: fuzzHas && coordFuzzResolves(in.Domains, func(s *hoststate.CoordinatorSeed) {
				s.SessionMode = m
				if m == api.SessionModeAsk {
					s.WorkflowState = hoststate.WorkflowAmbientIdle
				}
			}),
		}
		for _, st := range in.CoordinatorStates {
			if st.Excluded || st.SessionMode != m {
				continue
			}
			if obs, ok := in.DeliveredObservations[st.ID]; ok && obs.ResolvedSessionMode == string(m) {
				cRow.DeterministicDelivered = true
				cRow.WitnessStateID = st.ID
				cRow.ResolvedInputValue = obs.ResolvedSessionMode
				cRow.GuidanceRelevantResult = fmt.Sprintf("session_mode=%s surface=%s", obs.ResolvedSessionMode, st.TurnProfile.SurfaceID)
				break
			}
		}
		rows = append(rows, finalizeRow(cRow))

		wRow := DomainClassRow{
			InputDomain: "session_mode", ProductionSource: src, ValueOrClass: string(m), Recipient: "worker",
			FuzzSelectable: fuzzHas && workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) {
				s.ParentSessionMode = m
				if m == api.SessionModeAsk {
					s.AgentID = "repo-researcher"
					s.BranchView = hoststate.BranchMainGit
				}
			}),
		}
		for _, ws := range in.WorkerStates {
			if ws.Excluded || ws.ParentSessionMode != m {
				continue
			}
			if obs, ok := in.DeliveredObservations[ws.ID]; ok && obs.ResolvedSessionMode == string(m) {
				wRow.DeterministicDelivered = true
				wRow.WitnessStateID = ws.ID
				wRow.ResolvedInputValue = obs.ResolvedSessionMode
				wRow.GuidanceRelevantResult = fmt.Sprintf("worker_session_mode=%s agent=%s", obs.ResolvedSessionMode, ws.AgentID)
				break
			}
		}
		rows = append(rows, finalizeRow(wRow))

		for _, up := range []bool{false, true} {
			val := fmt.Sprintf("parent=%s,upgraded=%t", m, up)
			uRow := DomainClassRow{
				InputDomain: "parent_upgraded_ask_to_agent", ProductionSource: "internal/session:AskChildPreservation",
				ValueOrClass: val, Recipient: "worker",
			}
			if m != api.SessionModeAsk && up {
				uRow.ProductionImpossible = true
				uRow.ImpossibilityReason = "parent_upgrade_requires_initial_ask_mode"
				rows = append(rows, finalizeRow(uRow))
				continue
			}
			uRow.FuzzSelectable = workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) {
				s.ParentSessionMode = m
				s.ParentUpgradedAskToAgent = up
				if m == api.SessionModeAsk {
					s.AgentID = "repo-researcher"
					s.BranchView = hoststate.BranchMainGit
				}
			})
			for _, ws := range in.WorkerStates {
				if !ws.Excluded && ws.ParentSessionMode == m && ws.ParentUpgradedAskToAgent == up {
					if obs, ok := in.DeliveredObservations[ws.ID]; ok {
						uRow.DeterministicDelivered = true
						uRow.WitnessStateID = ws.ID
						uRow.ResolvedInputValue = val
						uRow.GuidanceRelevantResult = fmt.Sprintf("child_session_mode=%s", obs.ResolvedSessionMode)
						break
					}
				}
			}
			rows = append(rows, finalizeRow(uRow))
		}
	}
	return rows
}

func buildPostureAndOverlayRows(in HostStateInventoryInput) []DomainClassRow {
	var rows []DomainClassRow
	for _, p := range in.Domains.Postures {
		cRow := DomainClassRow{
			InputDomain: "session_posture", ProductionSource: "internal/session/profiles:LoadPostureRegistry",
			ValueOrClass: string(p.ID), Recipient: "coordinator",
			FuzzSelectable: coordFuzzResolves(in.Domains, func(s *hoststate.CoordinatorSeed) { s.Posture = p.ID }),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.Posture == p.ID {
				if _, ok := in.DeliveredObservations[st.ID]; ok {
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = string(p.ID)
					cRow.GuidanceRelevantResult = fmt.Sprintf("surface=%s", st.TurnProfile.SurfaceID)
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))

		wRow := DomainClassRow{
			InputDomain: "session_posture", ProductionSource: "internal/session/profiles:LoadPostureRegistry",
			ValueOrClass: string(p.ID), Recipient: "worker",
			FuzzSelectable: workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) { s.Posture = p.ID }),
		}
		for _, ws := range in.WorkerStates {
			if !ws.Excluded && ws.Posture == p.ID {
				if _, ok := in.DeliveredObservations[ws.ID]; ok {
					wRow.DeterministicDelivered = true
					wRow.WitnessStateID = ws.ID
					wRow.ResolvedInputValue = string(p.ID)
					wRow.GuidanceRelevantResult = fmt.Sprintf("agent=%s", ws.AgentID)
					break
				}
			}
		}
		rows = append(rows, finalizeRow(wRow))
	}

	for ovIdx, ov := range in.Domains.Overlays {
		cRow := DomainClassRow{
			InputDomain: "pack_overlay", ProductionSource: "test/contract/internal/hoststate:DiscoverPackOverlays",
			ValueOrClass: ov.ID, Recipient: "coordinator",
			FuzzSelectable: coordFuzzResolves(in.Domains, func(s *hoststate.CoordinatorSeed) { s.OverlayIndex = ovIdx }),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.PackOverlayID == ov.ID {
				if _, ok := in.DeliveredObservations[st.ID]; ok {
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = ov.ID
					cRow.GuidanceRelevantResult = fmt.Sprintf("catalog_rev=%s", ov.Catalog.Revision[:12])
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))

		wRow := DomainClassRow{
			InputDomain: "pack_overlay", ProductionSource: "test/contract/internal/hoststate:DiscoverPackOverlays",
			ValueOrClass: ov.ID, Recipient: "worker",
			FuzzSelectable: workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) { s.OverlayIndex = ovIdx }),
		}
		for _, ws := range in.WorkerStates {
			if ws.Overlay.ID == ov.ID && !ws.Excluded {
				if _, ok := in.DeliveredObservations[ws.ID]; ok {
					wRow.DeterministicDelivered = true
					wRow.WitnessStateID = ws.ID
					wRow.ResolvedInputValue = ov.ID
					wRow.GuidanceRelevantResult = fmt.Sprintf("agent=%s catalog_rev=%s", ws.AgentID, ov.Catalog.Revision[:12])
					break
				}
			}
		}
		rows = append(rows, finalizeRow(wRow))
	}
	return rows
}

func buildSurfaceAndWorkflowRows(in HostStateInventoryInput) []DomainClassRow {
	var rows []DomainClassRow
	for _, s := range in.Domains.Surfaces {
		cRow := DomainClassRow{
			InputDomain: "coordinator_surface", ProductionSource: "internal/coordinator/surfacecatalog:Load",
			ValueOrClass: s.ID, Recipient: "coordinator",
			FuzzSelectable: coordFuzzResolves(in.Domains, func(seed *hoststate.CoordinatorSeed) {
				seed.WorkflowState = hoststate.WorkflowActivePhase
				seed.SurfaceID = s.ID
				seed.SessionMode = api.SessionModeAgent
				seed.RootTopology = hoststate.RootsSingleGit
			}),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.TurnProfile.SurfaceID == s.ID {
				if _, ok := in.DeliveredObservations[st.ID]; ok {
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = s.ID
					cRow.GuidanceRelevantResult = fmt.Sprintf("surface_id=%s", st.TurnProfile.SurfaceID)
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))
	}
	rows = append(rows, finalizeRow(DomainClassRow{
		InputDomain: "coordinator_surface", ProductionSource: "internal/coordinator/promptloop/worker_tool_plan.go:surfaceAllowsTool",
		ValueOrClass: "any_coordinator_surface", Recipient: "worker",
		ProductionImpossible: true, ImpossibilityReason: "workers_have_no_coordinator_surface_compile",
	}))

	for _, wf := range in.Domains.WorkflowStates {
		cRow := DomainClassRow{
			InputDomain: "workflow_state", ProductionSource: "internal/coordinator/surface:ResolveTurnProfile",
			ValueOrClass: string(wf), Recipient: "coordinator",
			FuzzSelectable: slices.Contains(in.Domains.WorkflowStates, wf),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.WorkflowState == wf {
				if _, ok := in.DeliveredObservations[st.ID]; ok {
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = string(wf)
					cRow.GuidanceRelevantResult = fmt.Sprintf("surface=%s", st.TurnProfile.SurfaceID)
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))
	}
	return rows
}

func buildRootAndBranchRows(in HostStateInventoryInput) []DomainClassRow {
	const gitSrc = "internal/app/sessions/skill_reader.go:primaryRootHasGit,anyRootHasGit"
	var rows []DomainClassRow
	for _, pGit := range []bool{false, true} {
		for _, aGit := range []bool{false, true} {
			cls := fmt.Sprintf("multi_root:primary_git=%t,any_git=%t", pGit, aGit)
			for _, rec := range []string{"coordinator", "worker"} {
				r := DomainClassRow{
					InputDomain: "workspace_roots_multi_git", ProductionSource: gitSrc,
					ValueOrClass: cls, Recipient: rec,
				}
				if pGit && !aGit {
					r.ProductionImpossible = true
					r.ImpossibilityReason = "anyRootHasGit_includes_primaryRootHasGit"
					rows = append(rows, finalizeRow(r))
					continue
				}
				if rec == "coordinator" {
					r.FuzzSelectable = coordMultiGitFuzzSelectable(in.Domains, pGit, aGit)
					for _, st := range in.CoordinatorStates {
						if !st.Excluded && st.RootCount >= 2 && st.HasGit == pGit && st.HasAnyGitRoot == aGit {
							if obs, ok := in.DeliveredObservations[st.ID]; ok && obs.MultiRoot && obs.PrimaryRootGit == pGit && obs.AnyRootGit == aGit {
								r.DeterministicDelivered = true
								r.WitnessStateID = st.ID
								r.ResolvedInputValue = fmt.Sprintf("roots=%d topology=%s", st.RootCount, st.RootTopology)
								r.GuidanceRelevantResult = fmt.Sprintf("has_git=%t has_any_git_root=%t multi_root=%t wiring=%t",
									obs.PrimaryRootGit, obs.AnyRootGit, obs.MultiRoot, in.WiringRootGitPairs[cls])
								break
							}
						}
					}
				} else {
					r.FuzzSelectable = workerMultiGitFuzzSelectable(in.Domains, pGit, aGit)
					for _, ws := range in.WorkerStates {
						if !ws.Excluded && ws.RootCount >= 2 && ws.HasGit == pGit && ws.HasAnyGitRoot == aGit {
							if obs, ok := in.DeliveredObservations[ws.ID]; ok && obs.MultiRoot && obs.PrimaryRootGit == pGit && obs.AnyRootGit == aGit {
								r.DeterministicDelivered = true
								r.WitnessStateID = ws.ID
								r.ResolvedInputValue = fmt.Sprintf("roots=%d branch=%s secGit=%t", ws.RootCount, ws.BranchView, ws.SecondaryRootGit)
								r.GuidanceRelevantResult = fmt.Sprintf("has_git=%t has_any_git_root=%t multi_root=%t",
									obs.PrimaryRootGit, obs.AnyRootGit, obs.MultiRoot)
								break
							}
						}
					}
				}
				rows = append(rows, finalizeRow(r))
			}
		}
	}

	for _, bv := range []hoststate.WorkerBranchView{
		hoststate.BranchMainGit, hoststate.BranchMainNonGit,
		hoststate.BranchLinkedWorktreeGit, hoststate.BranchIsolatedWorker,
	} {
		bRow := DomainClassRow{
			InputDomain: "worker_branch_view", ProductionSource: "internal/app/sessions/skill_reader.go:OnWorkerBranch",
			ValueOrClass: string(bv), Recipient: "worker",
			FuzzSelectable: slices.Contains(in.Domains.WorkerBranches, bv),
		}
		for _, ws := range in.WorkerStates {
			if !ws.Excluded && ws.BranchView == bv {
				if _, ok := in.DeliveredObservations[ws.ID]; ok {
					bRow.DeterministicDelivered = true
					bRow.WitnessStateID = ws.ID
					bRow.ResolvedInputValue = string(bv)
					bRow.GuidanceRelevantResult = fmt.Sprintf("on_worker_branch=%t has_git=%t", ws.OnWorkerBranch, ws.HasGit)
					break
				}
			}
		}
		rows = append(rows, finalizeRow(bRow))
	}
	return rows
}

func buildLoadingAndAgentRows(in HostStateInventoryInput) []DomainClassRow {
	var rows []DomainClassRow
	for _, ld := range []hoststate.LoadingMode{hoststate.LoadingFloorOnly, hoststate.LoadingPartlyLoaded, hoststate.LoadingAllLoaded} {
		cRow := DomainClassRow{
			InputDomain: "deferred_tool_loading", ProductionSource: "internal/coordinator/promptloop:compileRuntimeToolPlan",
			ValueOrClass: string(ld), Recipient: "coordinator",
			FuzzSelectable: slices.Contains(in.Domains.LoadingModes, ld),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.Loading == ld {
				if obs, ok := in.DeliveredObservations[st.ID]; ok {
					if ld == hoststate.LoadingPartlyLoaded && (len(obs.ActivatedDeferredTools) == 0 || len(obs.RemainingDeferredTools) == 0) {
						continue
					}
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = string(ld)
					cRow.GuidanceRelevantResult = fmt.Sprintf("activated=%v remaining=%d", obs.ActivatedDeferredTools, len(obs.RemainingDeferredTools))
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))
	}

	for _, wld := range []string{"sticky_only", "partly_loaded", "all_loaded"} {
		mode := hoststate.WorkerLoadingMode(wld)
		wRow := DomainClassRow{
			InputDomain: "deferred_tool_loading", ProductionSource: "internal/coordinator/promptloop/worker_tool_plan.go:compileWorkerToolPlan",
			ValueOrClass: wld, Recipient: "worker",
			FuzzSelectable: slices.Contains(in.Domains.WorkerLoadings, mode) &&
				workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) { s.Loading = mode }),
		}
		for _, ws := range in.WorkerStates {
			if ws.Loading != mode {
				continue
			}
			if ws.Excluded {
				if wRow.ExclusionWitnessID == "" {
					wRow.ExclusionWitnessID = ws.ID + " (" + ws.ExcludedReason + ")"
				}
				continue
			}
			if obs, ok := in.DeliveredObservations[ws.ID]; ok {
				if wld == "partly_loaded" && (len(obs.ActivatedDeferredTools) == 0 || len(obs.RemainingDeferredTools) == 0) {
					continue
				}
				wRow.DeterministicDelivered = true
				wRow.WitnessStateID = ws.ID
				wRow.ResolvedInputValue = wld
				wRow.GuidanceRelevantResult = fmt.Sprintf("activated=%v remaining_deferred=%v wiring=%v",
					obs.ActivatedDeferredTools, obs.RemainingDeferredTools, in.WiringWorkerPartialTools)
				break
			}
		}
		rows = append(rows, finalizeRow(wRow))
	}

	if len(in.Domains.Overlays) > 0 {
		for _, ag := range in.Domains.Overlays[0].WorkerAgents {
			aRow := DomainClassRow{
				InputDomain: "worker_agent_profile", ProductionSource: "internal/agentdef:LoadEffectiveWithCatalog",
				ValueOrClass: ag.ID + ":" + ag.ToolProfile, Recipient: "worker",
				FuzzSelectable: workerFuzzResolves(in.Domains, func(s *hoststate.WorkerSeed) {
					s.AgentID = ag.ID
					s.BranchView = hoststate.BranchMainGit
				}),
			}
			for _, ws := range in.WorkerStates {
				if !ws.Excluded && ws.AgentID == ag.ID {
					if _, ok := in.DeliveredObservations[ws.ID]; ok {
						aRow.DeterministicDelivered = true
						aRow.WitnessStateID = ws.ID
						aRow.ResolvedInputValue = ag.ID
						aRow.GuidanceRelevantResult = fmt.Sprintf("profile=%s", ws.ToolProfile)
						break
					}
				}
			}
			rows = append(rows, finalizeRow(aRow))
		}
	}
	return rows
}

func buildEnvToggleRows(in HostStateInventoryInput) []DomainClassRow {
	var rows []DomainClassRow
	for _, b := range []bool{false, true} {
		val := fmt.Sprintf("%t", b)
		rows = append(rows,
			checkBoolToggle(in, "web_search", "internal/coordinator/promptloop:webSearchEnabled", val, "coordinator",
				func(st hoststate.CoordinatorState, obs DeliveredStateObservation) bool { return st.WebSearch == b && obs.WebSearchEnabled == b },
				func(s *hoststate.CoordinatorSeed) { s.WebSearch = b }, nil),
			checkBoolToggle(in, "web_search", "internal/coordinator/promptloop:webSearchEnabled", val, "worker", nil, nil,
				workerToggleSpec{
					match: func(ws hoststate.WorkerState, obs DeliveredStateObservation) bool { return ws.WebSearch == b && obs.WebSearchEnabled == b },
					mutate: func(s *hoststate.WorkerSeed) {
						s.AgentID = "implementer"
						s.WebSearch = b
					},
				}),
			checkBoolToggle(in, "vision", "internal/prompts:Reader.WithVision", val, "coordinator",
				func(st hoststate.CoordinatorState, obs DeliveredStateObservation) bool { return st.Vision == b && obs.VisionEnabled == b },
				func(s *hoststate.CoordinatorSeed) { s.Vision = b }, nil),
			checkBoolToggle(in, "vision", "internal/prompts:Reader.WithVision", val, "worker", nil, nil,
				workerToggleSpec{
					match:  func(ws hoststate.WorkerState, obs DeliveredStateObservation) bool { return ws.Vision == b && obs.VisionEnabled == b },
					mutate: func(s *hoststate.WorkerSeed) { s.Vision = b },
				}),
			checkBoolToggle(in, "command_external_hidden", "internal/session/profiles:CommandExternalHidden", val, "coordinator",
				func(st hoststate.CoordinatorState, obs DeliveredStateObservation) bool {
					return st.CommandExternalHidden == b && obs.CommandExternalHidden == b
				},
				func(s *hoststate.CoordinatorSeed) { s.CommandExternalHidden = b }, nil),
			checkBoolToggle(in, "command_external_hidden", "internal/session/profiles:CommandExternalHidden", val, "worker", nil, nil,
				workerToggleSpec{
					match: func(ws hoststate.WorkerState, obs DeliveredStateObservation) bool {
						return ws.CommandExternalHidden == b && obs.CommandExternalHidden == b
					},
					mutate: func(s *hoststate.WorkerSeed) { s.CommandExternalHidden = b },
				}),
		)
	}

	for _, tmp := range in.Domains.TempStates {
		cRow := DomainClassRow{
			InputDomain: "temp_roots", ProductionSource: "internal/session/profiles:WritableSharedTemp",
			ValueOrClass: string(tmp), Recipient: "coordinator",
			FuzzSelectable: slices.Contains(in.Domains.TempStates, tmp),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.TempState == tmp {
				if _, ok := in.DeliveredObservations[st.ID]; ok {
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = string(tmp)
					cRow.GuidanceRelevantResult = fmt.Sprintf("scratch=%t shared=%d", st.SessionScratchDir != "", len(st.SharedTempLocations))
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))
	}

	for _, mcp := range in.Domains.MCPStates {
		cRow := DomainClassRow{
			InputDomain: "mcp_attachment", ProductionSource: "internal/tools:PlanMCPTools",
			ValueOrClass: string(mcp), Recipient: "coordinator",
			FuzzSelectable: slices.Contains(in.Domains.MCPStates, mcp),
		}
		for _, st := range in.CoordinatorStates {
			if !st.Excluded && st.MCPState == mcp {
				if _, ok := in.DeliveredObservations[st.ID]; ok {
					cRow.DeterministicDelivered = true
					cRow.WitnessStateID = st.ID
					cRow.ResolvedInputValue = string(mcp)
					cRow.GuidanceRelevantResult = fmt.Sprintf("mcp=%s", st.MCPState)
					break
				}
			}
		}
		rows = append(rows, finalizeRow(cRow))
	}
	return rows
}

type workerToggleSpec struct {
	match  func(hoststate.WorkerState, DeliveredStateObservation) bool
	mutate func(*hoststate.WorkerSeed)
}

func checkBoolToggle(
	in HostStateInventoryInput,
	domain, src, val, rec string,
	coordMatch func(hoststate.CoordinatorState, DeliveredStateObservation) bool,
	coordMutate func(*hoststate.CoordinatorSeed),
	wSpec interface{},
) DomainClassRow {
	r := DomainClassRow{InputDomain: domain, ProductionSource: src, ValueOrClass: val, Recipient: rec}
	if rec == "coordinator" {
		r.FuzzSelectable = coordFuzzResolves(in.Domains, coordMutate)
		for _, st := range in.CoordinatorStates {
			if st.Excluded {
				continue
			}
			if obs, ok := in.DeliveredObservations[st.ID]; ok && coordMatch(st, obs) {
				r.DeterministicDelivered = true
				r.WitnessStateID = st.ID
				r.ResolvedInputValue = val
				r.GuidanceRelevantResult = fmt.Sprintf("markers=%v", obs.RenderedMarkers)
				break
			}
		}
		return finalizeRow(r)
	}
	spec := wSpec.(workerToggleSpec)
	r.FuzzSelectable = workerFuzzResolves(in.Domains, spec.mutate)
	for _, ws := range in.WorkerStates {
		if ws.Excluded {
			if r.ExclusionWitnessID == "" {
				r.ExclusionWitnessID = ws.ID + " (" + ws.ExcludedReason + ")"
			}
			continue
		}
		if obs, ok := in.DeliveredObservations[ws.ID]; ok && spec.match(ws, obs) {
			r.DeterministicDelivered = true
			r.WitnessStateID = ws.ID
			r.ResolvedInputValue = val
			r.GuidanceRelevantResult = fmt.Sprintf("markers=%v", obs.RenderedMarkers)
			break
		}
	}
	return finalizeRow(r)
}

func coordFuzzResolves(d *hoststate.SeedDomains, mutate func(*hoststate.CoordinatorSeed)) bool {
	for _, pSpec := range d.Postures {
		for _, loading := range []hoststate.LoadingMode{hoststate.LoadingFloorOnly, hoststate.LoadingAllLoaded} {
			seed := hoststate.CoordinatorSeed{
				OverlayIndex:    0,
				SessionMode:     api.SessionModeAgent,
				Posture:         pSpec.ID,
				WorkflowState:   hoststate.WorkflowAmbientIdle,
				SurfaceID:       "implement_investigate",
				RootTopology:    hoststate.RootsSingleGit,
				Loading:         loading,
				WebSearch:       true,
				ApprovalPosture: gate.PostureBalanced,
				TempState:       hoststate.TempScratchOnly,
				MCPState:        hoststate.MCPNone,
			}
			if mutate != nil {
				mutate(&seed)
			}
			st, err := hoststate.ResolveCoordinatorSeed(d, seed)
			if err == nil && !st.Excluded {
				return true
			}
		}
	}
	return false
}

func workerFuzzResolves(d *hoststate.SeedDomains, mutate func(*hoststate.WorkerSeed)) bool {
	for _, agentID := range []string{"implementer", "repo-researcher"} {
		for _, loading := range []hoststate.WorkerLoadingMode{hoststate.WorkerLoadingStickyOnly, hoststate.WorkerLoadingAllLoaded} {
			seed := hoststate.WorkerSeed{
				OverlayIndex:      0,
				AgentID:           agentID,
				Posture:           api.SessionPostureBuild,
				BranchView:        hoststate.BranchMainGit,
				RootCount:         1,
				Loading:           loading,
				ParentSessionMode: api.SessionModeAgent,
				WebSearch:         true,
				ApprovalPosture:   gate.PostureBalanced,
			}
			if mutate != nil {
				mutate(&seed)
			}
			st, err := hoststate.ResolveWorkerSeed(d, seed)
			if err == nil && !st.Excluded {
				return true
			}
		}
	}
	return false
}

func coordMultiGitFuzzSelectable(d *hoststate.SeedDomains, pGit, aGit bool) bool {
	for _, rt := range d.RootTopologies {
		st, err := hoststate.ResolveCoordinatorSeed(d, hoststate.CoordinatorSeed{
			SessionMode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
			WorkflowState: hoststate.WorkflowAmbientIdle, RootTopology: rt,
			Loading: hoststate.LoadingFloorOnly, WebSearch: true, ApprovalPosture: gate.PostureBalanced,
		})
		if err == nil && !st.Excluded && st.RootCount >= 2 && st.HasGit == pGit && st.HasAnyGitRoot == aGit {
			return true
		}
	}
	return false
}

func workerMultiGitFuzzSelectable(d *hoststate.SeedDomains, pGit, aGit bool) bool {
	for _, bv := range d.WorkerBranches {
		for _, secGit := range []bool{false, true} {
			st, err := hoststate.ResolveWorkerSeed(d, hoststate.WorkerSeed{
				AgentID: "implementer", Posture: api.SessionPostureBuild,
				BranchView: bv, RootCount: 2, SecondaryRootGit: secGit,
				Loading: hoststate.WorkerLoadingStickyOnly,
				ParentSessionMode: api.SessionModeAgent, WebSearch: true, ApprovalPosture: gate.PostureBalanced,
			})
			if err == nil && !st.Excluded && st.RootCount >= 2 && st.HasGit == pGit && st.HasAnyGitRoot == aGit {
				return true
			}
		}
	}
	return workerMultiGitExplicitHook(d, pGit, aGit)
}

var workerMultiGitExplicitHook = func(_ *hoststate.SeedDomains, _, _ bool) bool { return false }

// RegisterWorkerMultiGitFuzzCheck registers an optional hook checking multi-root
// non-Git worker selectability when WorkerSeed carries explicit root Git fields.
func RegisterWorkerMultiGitFuzzCheck(fn func(*hoststate.SeedDomains, bool, bool) bool) {
	if fn != nil {
		workerMultiGitExplicitHook = fn
	}
}

func buildReaderFactRows(in HostStateInventoryInput) ([]ReaderFactInventoryRow, error) {
	scalarMap := prompts.DeclaredReaderFactScalars()
	scalarNames := make([]string, 0, len(scalarMap))
	for k := range scalarMap {
		scalarNames = append(scalarNames, k)
	}
	slices.Sort(scalarNames)
	var out []ReaderFactInventoryRow
	for _, rec := range []string{"coordinator", "worker"} {
		for _, name := range scalarNames {
			seen := map[string]string{}
			if rec == "coordinator" {
				for _, st := range in.CoordinatorStates {
					if st.Excluded {
						continue
					}
					auth, ok := in.CoordinatorAuthorities[st.ID]
					if !ok {
						continue
					}
					cls := classifyFactValue(auth.Reader.Facts(), name)
					if _, exists := seen[cls]; !exists {
						seen[cls] = st.ID
					}
				}
			} else {
				for _, ws := range in.WorkerStates {
					if ws.Excluded {
						continue
					}
					auth, ok := in.WorkerAuthorities[ws.ID]
					if !ok {
						continue
					}
					cls := classifyFactValue(auth.Reader.Facts(), name)
					if _, exists := seen[cls]; !exists {
						seen[cls] = ws.ID
					}
				}
			}
			classes := make([]string, 0, len(seen))
			for k := range seen {
				classes = append(classes, k)
			}
			slices.Sort(classes)
			witnesses := make([]string, 0, len(classes))
			for _, k := range classes {
				witnesses = append(witnesses, seen[k])
			}
			out = append(out, ReaderFactInventoryRow{
				FactName:        name,
				FactType:        "reader_scalar",
				Recipient:       rec,
				ObservedClasses: classes,
				WitnessStateIDs: witnesses,
				ConstraintReason: readerFactSingleClassReason(name, rec, classes),
			})
		}
	}
	return out, nil
}

func classifyFactValue(facts map[string]any, name string) string {
	if name == "caps.vision" {
		if cm, ok := facts["caps"].(map[string]any); ok {
			return fmt.Sprintf("%v", cm["vision"])
		}
		return "false"
	}
	v, ok := facts[name]
	if !ok || v == nil {
		return "unset"
	}
	switch x := v.(type) {
	case bool:
		return fmt.Sprintf("%t", x)
	case int:
		if x == 0 {
			return "int:0"
		}
		if x == 1 {
			return "int:1"
		}
		return "int:>=2"
	case string:
		if x == "" {
			return "string:empty"
		}
		return "string:" + x
	case []string:
		if len(x) == 0 {
			return "list:0"
		}
		if len(x) == 1 {
			return "list:1"
		}
		return "list:>=2"
	default:
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice {
			if rv.Len() == 0 {
				return "list:0"
			}
			if rv.Len() == 1 {
				return "list:1"
			}
			return "list:>=2"
		}
		return fmt.Sprintf("%T", v)
	}
}

func readerFactSingleClassReason(name, rec string, classes []string) string {
	if len(classes) >= 2 {
		return ""
	}
	if rec == "coordinator" && (name == "is_coordinator" || name == "tool_profile" || name == "recall_may_widen") {
		return "coordinator_identity_constant_in_prompts_reader_facts"
	}
	if rec == "coordinator" && name == "on_worker_branch" {
		return "coordinator_never_runs_on_isolated_worker_branch"
	}
	if rec == "coordinator" && name == "visual_show_terminal_needs_request" {
		return "coordinator_surfaces_with_external_exec_do_not_defer_terminal_open_alone"
	}
	if name == "agent_tools" {
		return "every_admissible_turn_offers_at_least_two_tools"
	}
	if strings.HasPrefix(name, "can_request_git_") || name == "can_request_command_verify" {
		return "command_tool_is_sticky_or_immediate_whenever_addressable_in_stock_profiles"
	}
	if rec == "worker" && (name == "is_coordinator" || name == "recall_may_widen" ||
		name == "can_ask_user_secret" || name == "can_orient" || name == "has_workspace_roots" ||
		strings.HasPrefix(name, "can_spawn_") || strings.HasPrefix(name, "surface_") ||
		strings.HasPrefix(name, "card_") || name == "execution_mode" || name == "session_scratch_dir" ||
		strings.HasPrefix(name, "shared_temp_") || strings.HasPrefix(name, "progress_")) {
		return "worker_role_or_profile_structural_invariant_in_prompts_reader_facts"
	}
	if strings.HasPrefix(name, "native_edit_limit_") || strings.HasPrefix(name, "runner_") ||
		name == "visual_review_cadence" || name == "reject_codes" ||
		name == "read_globs" || name == "write_globs" || name == "lane_s_sync_tools" ||
		name == "command_output_files" {
		return "catalog_or_platform_constant_across_turn_states"
	}
	return ""
}

func buildBoundaryPartitions(ledger *InteractionLedger) ([]BoundaryPartitionRow, error) {
	specs, err := hoststate.AnchorCatalogSpecs()
	if err != nil {
		return nil, err
	}
	predByVar := map[string]map[string]bool{}
	if ledger != nil {
		for _, site := range ledger.DiscoveredInstructions {
			for _, g := range site.Guards {
				for _, occ := range site.OccurrenceFacts {
					short := strings.TrimPrefix(occ, "paintedwolf.")
					if predByVar[short] == nil {
						predByVar[short] = map[string]bool{}
					}
					predByVar[short][g.Expression] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(specs))
	for id := range specs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out []BoundaryPartitionRow
	for _, id := range ids {
		sp := specs[id]
		envs, envErr := hoststate.AnchorEnvelopes(id)
		if envErr != nil {
			return nil, envErr
		}
		for _, v := range sp.Vars {
			seen := map[string]bool{}
			for _, m := range envs {
				seen[classifyEnvelopeValue(m[v.Name], v.Type)] = true
			}
			out = append(out, BoundaryPartitionRow{
				AnchorID:          id,
				VarName:           v.Name,
				VarType:           v.Type,
				ShippedPredicates: sortedKeysOf(predByVar[v.Name]),
				GeneratedClasses:  sortedKeysOf(seen),
			})
		}
	}
	return out, nil
}

func classifyEnvelopeValue(val any, typ string) string {
	if val == nil {
		return "nil"
	}
	switch typ {
	case "bool":
		return fmt.Sprintf("bool:%v", val)
	case "int":
		n, _ := val.(int)
		if n == 0 {
			return "int:0"
		}
		if n == 1 {
			return "int:1"
		}
		return fmt.Sprintf("int:%d", n)
	case "string":
		s, _ := val.(string)
		if s == "" {
			return "string:empty"
		}
		return "string:nonempty"
	case "string[]":
		sl, _ := val.([]string)
		return fmt.Sprintf("len:%d", len(sl))
	case "object":
		return "object:present"
	case "object[]":
		sl, _ := val.([]map[string]any)
		return fmt.Sprintf("len:%d", len(sl))
	default:
		return fmt.Sprintf("%T", val)
	}
}

func reconcileInstructionProducers(in HostStateInventoryInput) (matched, unmatched []string, err error) {
	if in.Ledger == nil {
		return nil, nil, nil
	}
	specs, err := hoststate.AnchorCatalogSpecs()
	if err != nil {
		return nil, nil, err
	}
	anchorVars := map[string]bool{}
	for _, sp := range specs {
		for _, v := range sp.Vars {
			anchorVars[v.Name] = true
		}
	}
	oarFacts := map[string]bool{}
	if decl := oar.InstalledEngineDeclaration(); decl != nil {
		for _, hf := range decl.HostFacts {
			oarFacts[hf.Name] = true
		}
	}
	pg, err := DefaultProducerGraph()
	if err != nil {
		return nil, nil, err
	}
	matchedSet := map[string]bool{}
	unmatchedSet := map[string]bool{}
	for _, site := range in.Ledger.DiscoveredInstructions {
		allFacts := append(slices.Clone(site.CapabilityFacts), site.OccurrenceFacts...)
		for _, f := range allFacts {
			tr := pg.TraceFactForSite(f, site)
			if len(tr.ProducerSites) > 0 {
				for _, ps := range tr.ProducerSites {
					matchedSet[f+"@"+ps] = true
				}
				continue
			}
			short := strings.TrimPrefix(f, "paintedwolf.")
			if anchorVars[f] || anchorVars[short] || oarFacts[f] || oarFacts[short] ||
				f == "tool" || f == "agent" || f == "anchor" {
				matchedSet[f+"@catalog_decl"] = true
				continue
			}
			unmatchedSet[site.ID+" -> "+f] = true
		}
	}
	return sortedKeysOf(matchedSet), sortedKeysOf(unmatchedSet), nil
}
