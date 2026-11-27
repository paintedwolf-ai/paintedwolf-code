package interactions

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// Recipients of a delivered state.
const (
	RecipientCoordinator = "coordinator"
	RecipientWorker      = "worker"
)

// DomainClassRow records coverage of one production input value or class on
// one recipient. Raw enum selection stays separate from the Boolean outcome it
// feeds, so equal outcomes cannot hide an omitted value.
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
	ResolvedInputValue     string `json:"resolved_input_value,omitempty"`
	GuidanceRelevantResult string `json:"guidance_relevant_result,omitempty"`
}

// ReaderFactInventoryRow records the observed value classes of one declared
// reader fact on one recipient.
type ReaderFactInventoryRow struct {
	FactName         string   `json:"fact_name"`
	Recipient        string   `json:"recipient"`
	ObservedClasses  []string `json:"observed_classes"`
	WitnessStateIDs  []string `json:"witness_state_ids"`
	ConstraintSource string   `json:"constraint_source,omitempty"`
}

// BoundaryPartitionRow records the boundary classes generated for one anchor
// envelope variable and the shipped predicates that read it.
type BoundaryPartitionRow struct {
	AnchorID          string   `json:"anchor_id"`
	VarName           string   `json:"var_name"`
	VarType           string   `json:"var_type"`
	ShippedPredicates []string `json:"shipped_predicates,omitempty"`
	GeneratedClasses  []string `json:"generated_classes"`
}

// InventoryGap is one unexplained omission or unmatched producer.
type InventoryGap struct {
	Category         string `json:"category"`
	InputDomain      string `json:"input_domain"`
	ValueOrClass     string `json:"value_or_class"`
	Recipient        string `json:"recipient"`
	ProductionSource string `json:"production_source"`
	Detail           string `json:"detail"`
}

// HostStateInventoryInput holds the candidate sources the inventory joins.
type HostStateInventoryInput struct {
	Catalog           *extpacks.EffectiveCatalog
	Domains           *hoststate.SeedDomains
	CoordinatorStates []hoststate.CoordinatorState
	WorkerStates      []hoststate.WorkerState
	Sites             []InstructionSite
	Producers         ProducerJoin
}

// HostStateInventoryReport joins current authority inputs, reader facts,
// envelope partitions and instruction producers, and lists unexplained gaps.
type HostStateInventoryReport struct {
	CatalogRevision        string                   `json:"catalog_revision"`
	TotalCoordinatorStates int                      `json:"total_coordinator_states"`
	TotalWorkerStates      int                      `json:"total_worker_states"`
	TotalInstructionSites  int                      `json:"total_instruction_sites"`
	TotalAnchorSpecs       int                      `json:"total_anchor_specs"`
	ObservedCoordinators   int                      `json:"observed_coordinators"`
	ObservedWorkers        int                      `json:"observed_workers"`
	DomainClasses          []DomainClassRow         `json:"domain_classes"`
	ReaderFactCoverage     []ReaderFactInventoryRow `json:"reader_fact_coverage"`
	BoundaryPartitions     []BoundaryPartitionRow   `json:"boundary_partitions"`
	MatchedProducerInputs  []string                 `json:"matched_producer_inputs"`
	UnmatchedProducers     []string                 `json:"unmatched_producers,omitempty"`
	UnexplainedGaps        []InventoryGap           `json:"unexplained_gaps,omitempty"`
}

// stateObservation is what one state's production Reader delivers for the
// inputs the inventory tracks.
type stateObservation struct {
	SessionMode           string
	ReviewsConfinedWrites bool
	HasGit                bool
	HasAnyGitRoot         bool
	MultiRoot             bool
	RootCount             int
	WebSearch             bool
	Vision                bool
	ExternalHidden        bool
	Activated             []string
	Remaining             []string
}

func observe(loaded map[string]bool, auth hoststate.TurnAuthority) stateObservation {
	facts := auth.Reader.Facts()
	flag := func(name string) bool { v, _ := facts[name].(bool); return v }
	obs := stateObservation{
		ReviewsConfinedWrites: flag("reviews_confined_writes"),
		HasGit:                flag("has_git"),
		HasAnyGitRoot:         flag("has_any_git_root"),
		MultiRoot:             flag("multi_root"),
		WebSearch:             flag("web_search_enabled"),
		ExternalHidden:        flag("command_external_hidden"),
		Remaining:             auth.Reader.Requestable(),
	}
	obs.SessionMode, _ = facts["session_mode"].(string)
	obs.RootCount, _ = facts["root_count"].(int)
	if caps, ok := facts["caps"].(map[string]any); ok {
		obs.Vision, _ = caps["vision"].(bool)
	}
	for name, on := range loaded {
		if on && slices.Contains(auth.Offered, name) {
			obs.Activated = append(obs.Activated, name)
		}
	}
	slices.Sort(obs.Activated)
	slices.Sort(obs.Remaining)
	return obs
}

// HostStateInventory accumulates what each state's production Reader
// delivers; it keeps observations and fact classes, never the Readers.
type HostStateInventory struct {
	in      HostStateInventoryInput
	coord   map[string]stateObservation
	workers map[string]stateObservation
	// classes maps recipient, fact and value class to the first witness state.
	classes map[string]map[string]map[string]string
}

// NewHostStateInventory starts an inventory over in.
func NewHostStateInventory(in HostStateInventoryInput) *HostStateInventory {
	return &HostStateInventory{
		in: in, coord: map[string]stateObservation{}, workers: map[string]stateObservation{},
		classes: map[string]map[string]map[string]string{RecipientCoordinator: {}, RecipientWorker: {}},
	}
}

// ObserveCoordinator records the authority resolved for st.
func (inv *HostStateInventory) ObserveCoordinator(st hoststate.CoordinatorState, auth hoststate.TurnAuthority) {
	inv.coord[st.ID] = observe(st.Loaded, auth)
	inv.recordClasses(RecipientCoordinator, st.ID, auth.Reader.Facts())
}

// ObserveWorker records the authority resolved for ws.
func (inv *HostStateInventory) ObserveWorker(ws hoststate.WorkerState, auth hoststate.TurnAuthority) {
	inv.workers[ws.ID] = observe(ws.Loaded, auth)
	inv.recordClasses(RecipientWorker, ws.ID, auth.Reader.Facts())
}

func (inv *HostStateInventory) recordClasses(rec, stateID string, facts map[string]any) {
	for name := range prompts.DeclaredReaderFactScalars() {
		cls := classifyFactValue(facts, name)
		byClass := inv.classes[rec][name]
		if byClass == nil {
			byClass = map[string]string{}
			inv.classes[rec][name] = byClass
		}
		if _, ok := byClass[cls]; !ok {
			byClass[cls] = stateID
		}
	}
}

// coordWitness returns the first delivered coordinator state match accepts.
func (inv *HostStateInventory) coordWitness(match func(hoststate.CoordinatorState, stateObservation) bool) (hoststate.CoordinatorState, stateObservation, bool) {
	for _, st := range inv.in.CoordinatorStates {
		if obs, ok := inv.coord[st.ID]; ok && match(st, obs) {
			return st, obs, true
		}
	}
	return hoststate.CoordinatorState{}, stateObservation{}, false
}

// workerWitness returns the first delivered worker state match accepts.
func (inv *HostStateInventory) workerWitness(match func(hoststate.WorkerState, stateObservation) bool) (hoststate.WorkerState, stateObservation, bool) {
	for _, ws := range inv.in.WorkerStates {
		if obs, ok := inv.workers[ws.ID]; ok && match(ws, obs) {
			return ws, obs, true
		}
	}
	return hoststate.WorkerState{}, stateObservation{}, false
}

// Report derives the coverage report from the production registries, the
// observed states and the producer join.
func (inv *HostStateInventory) Report(ctx context.Context) (*HostStateInventoryReport, error) {
	in := inv.in
	if in.Domains == nil || in.Catalog == nil {
		return nil, fmt.Errorf("domains and effective catalog required")
	}
	specs, err := hoststate.AnchorCatalogSpecs()
	if err != nil {
		return nil, err
	}
	boundaries, err := buildBoundaryPartitions(specs, in.Sites)
	if err != nil {
		return nil, err
	}
	rep := &HostStateInventoryReport{
		CatalogRevision:        in.Catalog.Revision,
		TotalCoordinatorStates: len(in.CoordinatorStates),
		TotalWorkerStates:      len(in.WorkerStates),
		TotalInstructionSites:  len(in.Sites),
		TotalAnchorSpecs:       len(specs),
		ObservedCoordinators:   len(inv.coord),
		ObservedWorkers:        len(inv.workers),
		DomainClasses:          inv.domainRows(ctx),
		ReaderFactCoverage:     inv.readerFactRows(),
		BoundaryPartitions:     boundaries,
		MatchedProducerInputs:  in.Producers.Matched,
		UnmatchedProducers:     in.Producers.Unmatched,
	}
	rep.UnexplainedGaps = computeInventoryGaps(rep)
	return rep, nil
}

func (inv *HostStateInventory) domainRows(ctx context.Context) []DomainClassRow {
	var rows []DomainClassRow
	rows = append(rows, inv.approvalRows(ctx)...)
	rows = append(rows, inv.sessionModeRows(ctx)...)
	rows = append(rows, inv.postureAndOverlayRows(ctx)...)
	rows = append(rows, inv.surfaceAndWorkflowRows()...)
	rows = append(rows, inv.rootAndBranchRows(ctx)...)
	rows = append(rows, inv.loadingAndAgentRows(ctx)...)
	rows = append(rows, inv.envToggleRows(ctx)...)
	for i := range rows {
		rows[i] = finalizeRow(rows[i])
	}
	slices.SortFunc(rows, func(a, b DomainClassRow) int {
		return strings.Compare(
			a.InputDomain+"|"+a.Recipient+"|"+a.ValueOrClass,
			b.InputDomain+"|"+b.Recipient+"|"+b.ValueOrClass,
		)
	})
	return rows
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

func row(domain, source, value, recipient string) DomainClassRow {
	return DomainClassRow{InputDomain: domain, ProductionSource: source, ValueOrClass: value, Recipient: recipient}
}

func impossible(domain, source, value, recipient, reason string) DomainClassRow {
	r := row(domain, source, value, recipient)
	r.ProductionImpossible, r.ImpossibilityReason = true, reason
	return r
}

// witness marks r delivered by stateID with the resolved input and result.
func (r DomainClassRow) witness(stateID, resolved, result string) DomainClassRow {
	r.DeterministicDelivered, r.WitnessStateID = true, stateID
	r.ResolvedInputValue, r.GuidanceRelevantResult = resolved, result
	return r
}
