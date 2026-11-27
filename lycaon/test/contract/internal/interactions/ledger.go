package interactions

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// WitnessCellExclusion records an impossible interaction tuple excluded by a
// concrete production constraint.
type WitnessCellExclusion struct {
	Tuple            string `json:"tuple"`
	ProductionReason string `json:"production_reason"`
}

// WitnessGap records an unasserted realizable interaction cell for disposition.
type WitnessGap struct {
	InstructionID    string `json:"instruction_id"`
	SourceSpan       string `json:"source_span"`
	StateTuple       string `json:"state_tuple"`
	MissingAssertion string `json:"missing_assertion"`
	AffectedAction   string `json:"affected_action"`
}

// WitnessLedgerRow summarizes interaction coverage for one paired usefulness or
// multi-way interaction witness, separating independent expected-action assertions
// from state visits.
type WitnessLedgerRow struct {
	WitnessID                 string                 `json:"witness_id"`
	InstructionID             string                 `json:"instruction_id"`
	SourceSpan                string                 `json:"source_span"`
	AffectedAction            string                 `json:"affected_action"`
	JointCapabilityFacts      []string               `json:"joint_capability_facts"`
	JointOccurrenceFacts      []string               `json:"joint_occurrence_facts"`
	DerivedPrimitiveInputs    []string               `json:"derived_primitive_inputs"`
	StateVisits               int                    `json:"state_visits"`
	DeduplicatedProjections   int                    `json:"deduplicated_projections"`
	RealizableRelevantCells   []string               `json:"realizable_relevant_cells"`
	IndependentlyAsserted     []string               `json:"independently_asserted_cells"`
	UnassertedRealizableCells []string               `json:"unasserted_realizable_cells,omitempty"`
	Exclusions                []WitnessCellExclusion `json:"exclusions,omitempty"`
}

// CompactInstructionTrace summarizes the AST guard -> producer -> primitive-input
// derivation chain for a worked witness (e.g. job-wait or archive recovery).
type CompactInstructionTrace struct {
	Name            string      `json:"name"`
	ContributionID  string      `json:"contribution_id"`
	DeliverySite    string      `json:"delivery_site"`
	Actions         []string    `json:"actions"`
	GuardSummary    []string    `json:"guard_summary"`
	CapabilityFacts []FactTrace `json:"capability_facts"`
	OccurrenceFacts []FactTrace `json:"occurrence_facts"`
}

// EdgeScope identifies one (EdgeID, InstructionID, Recipient, Action) obligation
// created when a discovered instruction depends on a fact with an UnresolvedEdge.
type EdgeScope struct {
	EdgeID           string `json:"edge_id"`
	Fact             string `json:"fact"`
	ProducerLocation string `json:"producer_location"`
	Reason           string `json:"reason"`
	InstructionID    string `json:"instruction_id"`
	Recipient        string `json:"recipient"`
	AffectedAction   string `json:"affected_action"`
}

// ExecutedEdgeAssertion records an actually executed test assertion that checked
// AffectedAction or its explicit omission for InstructionID on Recipient under
// InputState while exercising EdgeID, closing the matching EdgeScope.
type ExecutedEdgeAssertion struct {
	EdgeID         string `json:"edge_id"`
	InstructionID  string `json:"instruction_id"`
	Recipient      string `json:"recipient"`
	InputState     string `json:"input_state"`
	AssertedAction string `json:"asserted_action"`
	CheckedOutcome string `json:"checked_outcome"`
	TestName       string `json:"test_name"`
}

// ApprovedEdgeExclusion records an explicitly source-grounded production exclusion
// for an EdgeScope that cannot be realized in the target test scope.
type ApprovedEdgeExclusion struct {
	EdgeID           string `json:"edge_id"`
	InstructionID    string `json:"instruction_id"`
	Recipient        string `json:"recipient"`
	AffectedAction   string `json:"affected_action"`
	SourceConstraint string `json:"source_constraint"`
}

// InteractionLedger is the sorted, deterministic source-derived interaction
// inventory, graph metrics, witness summary, compact traces, and unresolved-edge
// reconciliation report.
type InteractionLedger struct {
	ProducerGraphStats     GraphStats                `json:"producer_graph_stats"`
	TotalDiscoveredSites   int                       `json:"total_discovered_sites"`
	SitesByFamily          map[string]int            `json:"sites_by_family"`
	DiscoveredInstructions []InstructionSite         `json:"discovered_instructions"`
	CompactTraces          []CompactInstructionTrace `json:"compact_traces"`
	WitnessRows            []WitnessLedgerRow        `json:"witness_rows"`
	UnresolvedEdges        []UnresolvedEdge          `json:"unresolved_edges"`
	RequiredEdgeScopes     []EdgeScope               `json:"required_edge_scopes,omitempty"`
	ExecutedEdgeAssertions []ExecutedEdgeAssertion   `json:"executed_edge_assertions,omitempty"`
	ApprovedEdgeExclusions []ApprovedEdgeExclusion   `json:"approved_edge_exclusions,omitempty"`
	OpenEdgeGaps           []EdgeScope               `json:"open_edge_gaps,omitempty"`
	UnexplainedGaps        []WitnessGap              `json:"unexplained_gaps,omitempty"`
}

func recipientsForSite(site InstructionSite) []string {
	switch {
	case strings.HasPrefix(site.DeliverySite, "coordinator_"):
		return []string{"coordinator"}
	case strings.HasPrefix(site.DeliverySite, "standing_persona:coordinator"):
		return []string{"coordinator"}
	case strings.HasPrefix(site.DeliverySite, "standing_persona:"):
		return []string{"worker"}
	case strings.HasPrefix(site.DeliverySite, "oar:worker."):
		return []string{"worker"}
	case strings.HasPrefix(site.DeliverySite, "oar:credential.assignment:"):
		return []string{"coordinator", "worker"}
	case site.ContributionID == "guidance/command-jobs":
		return []string{"coordinator", "worker"}
	default:
		return []string{"coordinator"}
	}
}

// BuildInteractionLedger discovers every actionable site in eff, traces each
// referenced capability and occurrence fact through pg (using DefaultProducerGraph
// when pg is nil), records graph stats, and computes required unresolved-edge scopes.
func BuildInteractionLedger(
	eff *extpacks.EffectiveCatalog,
	schemas *toolschema.Config,
	names *toolvocab.Catalog,
	pg *ProducerGraph,
) (*InteractionLedger, error) {
	if pg == nil {
		var err error
		pg, err = DefaultProducerGraph()
		if err != nil {
			return nil, fmt.Errorf("load default producer graph: %w", err)
		}
	}
	sites, err := DiscoverCatalogInstructionsWithGraph(eff, schemas, names, pg)
	if err != nil {
		return nil, err
	}

	byFam := map[string]int{}
	unresolvedMap := map[string]UnresolvedEdge{}
	scopeMap := map[string]EdgeScope{}

	for i := range sites {
		byFam[sites[i].Family]++
		primSet := map[string]bool{}
		unresFacts := map[string]bool{}
		allFacts := append(slices.Clone(sites[i].CapabilityFacts), sites[i].OccurrenceFacts...)
		recips := recipientsForSite(sites[i])
		for _, f := range allFacts {
			tr := pg.TraceFactForSite(f, sites[i])
			for _, inp := range tr.PrimitiveInputs {
				primSet[inp] = true
			}
			for _, u := range tr.Unresolved {
				unresFacts[f] = true
				unresolvedMap[u.EdgeID] = u
				for _, rec := range recips {
					sKey := u.EdgeID + "|" + sites[i].ID + "|" + rec + "|" + sites[i].Action
					scopeMap[sKey] = EdgeScope{
						EdgeID:           u.EdgeID,
						Fact:             u.Fact,
						ProducerLocation: u.ProducerLocation,
						Reason:           u.Reason,
						InstructionID:    sites[i].ID,
						Recipient:        rec,
						AffectedAction:   sites[i].Action,
					}
				}
			}
		}
		sites[i].PrimitiveInputs = sortedKeysOf(primSet)
		sites[i].UnresolvedFacts = sortedKeysOf(unresFacts)
	}

	var uList []UnresolvedEdge
	for _, u := range unresolvedMap {
		uList = append(uList, u)
	}
	slices.SortFunc(uList, func(a, b UnresolvedEdge) int {
		return strings.Compare(a.EdgeID, b.EdgeID)
	})

	var scopes []EdgeScope
	for _, sc := range scopeMap {
		scopes = append(scopes, sc)
	}
	sortEdgeScopes(scopes)

	traces := []CompactInstructionTrace{
		buildCompactTrace("job-wait", "guidance/command-jobs", sites, pg),
		buildCompactTrace("archive-recovery", "policy/EXTRACT_FORMAT_UNSUPPORTED#fix", sites, pg),
	}

	l := &InteractionLedger{
		ProducerGraphStats:     pg.Stats(),
		TotalDiscoveredSites:   len(sites),
		SitesByFamily:          byFam,
		DiscoveredInstructions: sites,
		CompactTraces:          traces,
		UnresolvedEdges:        uList,
		RequiredEdgeScopes:     scopes,
	}
	l.ReconcileUnresolvedEdges()
	return l, nil
}

func sortEdgeScopes(scopes []EdgeScope) {
	slices.SortFunc(scopes, func(a, b EdgeScope) int {
		return strings.Compare(
			a.EdgeID+"|"+a.InstructionID+"|"+a.Recipient+"|"+a.AffectedAction,
			b.EdgeID+"|"+b.InstructionID+"|"+b.Recipient+"|"+b.AffectedAction,
		)
	})
}

func buildCompactTrace(name, contribID string, sites []InstructionSite, pg *ProducerGraph) CompactInstructionTrace {
	var delivery string
	actions := map[string]bool{}
	guards := map[string]bool{}
	capNames := map[string]bool{}
	occNames := map[string]bool{}

	for _, s := range sites {
		if s.ContributionID != contribID {
			continue
		}
		if delivery == "" {
			delivery = s.DeliverySite
		}
		actions[s.Action] = true
		for _, g := range s.Guards {
			prefix := ""
			if g.Negated {
				prefix = "!"
			}
			guards[prefix+"("+g.Expression+")"] = true
		}
		for _, c := range s.CapabilityFacts {
			capNames[c] = true
		}
		for _, o := range s.OccurrenceFacts {
			occNames[o] = true
		}
	}

	var capTraces, occTraces []FactTrace
	for _, c := range sortedKeysOf(capNames) {
		capTraces = append(capTraces, pg.TraceFact(c))
	}
	for _, o := range sortedKeysOf(occNames) {
		occTraces = append(occTraces, pg.TraceFact(o))
	}

	return CompactInstructionTrace{
		Name:            name,
		ContributionID:  contribID,
		DeliverySite:    delivery,
		Actions:         sortedKeysOf(actions),
		GuardSummary:    sortedKeysOf(guards),
		CapabilityFacts: capTraces,
		OccurrenceFacts: occTraces,
	}
}

// RecordExecutedEdgeAssertion records an executed assertion closing matching EdgeScope(s).
func (l *InteractionLedger) RecordExecutedEdgeAssertion(a ExecutedEdgeAssertion) {
	if slices.Contains(l.ExecutedEdgeAssertions, a) {
		return
	}
	l.ExecutedEdgeAssertions = append(l.ExecutedEdgeAssertions, a)
	slices.SortFunc(l.ExecutedEdgeAssertions, func(x, y ExecutedEdgeAssertion) int {
		return strings.Compare(
			x.EdgeID+"|"+x.InstructionID+"|"+x.Recipient+"|"+x.AssertedAction+"|"+x.CheckedOutcome+"|"+x.InputState,
			y.EdgeID+"|"+y.InstructionID+"|"+y.Recipient+"|"+y.AssertedAction+"|"+y.CheckedOutcome+"|"+y.InputState,
		)
	})
	l.ReconcileUnresolvedEdges()
}

// RecordApprovedEdgeExclusion records a source-grounded production exclusion for an EdgeScope.
func (l *InteractionLedger) RecordApprovedEdgeExclusion(e ApprovedEdgeExclusion) {
	l.ApprovedEdgeExclusions = append(l.ApprovedEdgeExclusions, e)
	slices.SortFunc(l.ApprovedEdgeExclusions, func(x, y ApprovedEdgeExclusion) int {
		return strings.Compare(
			x.EdgeID+"|"+x.InstructionID+"|"+x.Recipient+"|"+x.AffectedAction,
			y.EdgeID+"|"+y.InstructionID+"|"+y.Recipient+"|"+y.AffectedAction,
		)
	})
	l.ReconcileUnresolvedEdges()
}

// ReconcileUnresolvedEdges joins RequiredEdgeScopes against ExecutedEdgeAssertions
// and ApprovedEdgeExclusions by exact (EdgeID, InstructionID, Recipient, Action)
// identity, retaining any unjoined scope in OpenEdgeGaps.
func (l *InteractionLedger) ReconcileUnresolvedEdges() {
	closed := map[string]bool{}
	for _, a := range l.ExecutedEdgeAssertions {
		if strings.TrimSpace(a.EdgeID) == "" || strings.TrimSpace(a.InstructionID) == "" ||
			strings.TrimSpace(a.Recipient) == "" || strings.TrimSpace(a.InputState) == "" ||
			strings.TrimSpace(a.AssertedAction) == "" || strings.TrimSpace(a.CheckedOutcome) == "" ||
			strings.TrimSpace(a.TestName) == "" {
			continue
		}
		key := a.EdgeID + "|" + a.InstructionID + "|" + a.Recipient + "|" + a.AssertedAction
		closed[key] = true
	}
	for _, ex := range l.ApprovedEdgeExclusions {
		if strings.TrimSpace(ex.EdgeID) == "" || strings.TrimSpace(ex.InstructionID) == "" ||
			strings.TrimSpace(ex.Recipient) == "" || strings.TrimSpace(ex.AffectedAction) == "" ||
			strings.TrimSpace(ex.SourceConstraint) == "" {
			continue
		}
		key := ex.EdgeID + "|" + ex.InstructionID + "|" + ex.Recipient + "|" + ex.AffectedAction
		closed[key] = true
	}
	var open []EdgeScope
	for _, sc := range l.RequiredEdgeScopes {
		key := sc.EdgeID + "|" + sc.InstructionID + "|" + sc.Recipient + "|" + sc.AffectedAction
		if !closed[key] {
			open = append(open, sc)
		}
	}
	sortEdgeScopes(open)
	l.OpenEdgeGaps = open
}

// Clone returns a deep copy of l for negative-control mutation tests.
func (l *InteractionLedger) Clone() *InteractionLedger {
	raw, _ := json.Marshal(l)
	var cp InteractionLedger
	_ = json.Unmarshal(raw, &cp)
	cp.ReconcileUnresolvedEdges()
	return &cp
}

// AddWitnessRow appends and normalizes one witness coverage row, populating any
// unasserted realizable cells into UnexplainedGaps.
func (l *InteractionLedger) AddWitnessRow(row WitnessLedgerRow) {
	slices.Sort(row.RealizableRelevantCells)
	slices.Sort(row.IndependentlyAsserted)
	assertedSet := map[string]bool{}
	for _, c := range row.IndependentlyAsserted {
		assertedSet[c] = true
	}
	var unasserted []string
	for _, cell := range row.RealizableRelevantCells {
		if !assertedSet[cell] {
			unasserted = append(unasserted, cell)
			l.UnexplainedGaps = append(l.UnexplainedGaps, WitnessGap{
				InstructionID:    row.InstructionID,
				SourceSpan:       row.SourceSpan,
				StateTuple:       cell,
				MissingAssertion: "independent_expected_action_check",
				AffectedAction:   row.AffectedAction,
			})
		}
	}
	row.UnassertedRealizableCells = unasserted
	slices.SortFunc(row.Exclusions, func(a, b WitnessCellExclusion) int {
		return strings.Compare(a.Tuple, b.Tuple)
	})
	l.WitnessRows = append(l.WitnessRows, row)
	slices.SortFunc(l.WitnessRows, func(a, b WitnessLedgerRow) int {
		return strings.Compare(a.WitnessID, b.WitnessID)
	})
	slices.SortFunc(l.UnexplainedGaps, func(a, b WitnessGap) int {
		return strings.Compare(a.InstructionID+"|"+a.StateTuple, b.InstructionID+"|"+b.StateTuple)
	})
}

// ValidateZeroUnexplainedGaps fails qualification if any realizable relevant
// witness cell lacks an independent expected-action assertion OR if any
// unresolved producer edge scope lacks an exact executed assertion or approved exclusion.
func (l *InteractionLedger) ValidateZeroUnexplainedGaps() error {
	l.ReconcileUnresolvedEdges()
	if len(l.UnexplainedGaps) > 0 {
		g := l.UnexplainedGaps[0]
		return fmt.Errorf("unexplained interaction gap (%d total): instruction=%s span=%s tuple=%s action=%s",
			len(l.UnexplainedGaps), g.InstructionID, g.SourceSpan, g.StateTuple, g.AffectedAction)
	}
	if len(l.OpenEdgeGaps) > 0 {
		eg := l.OpenEdgeGaps[0]
		return fmt.Errorf("unclosed unresolved producer edge (%d open of %d scopes): edge=%s instruction=%s recipient=%s action=%s",
			len(l.OpenEdgeGaps), len(l.RequiredEdgeScopes), eg.EdgeID, eg.InstructionID, eg.Recipient, eg.AffectedAction)
	}
	return nil
}

// MarshalPrettyJSON serializes the interaction ledger in deterministic indented JSON.
func (l *InteractionLedger) MarshalPrettyJSON() ([]byte, error) {
	l.ReconcileUnresolvedEdges()
	return json.MarshalIndent(l, "", "  ")
}

// RenderMarkdown renders a concise human-readable summary of graph metrics,
// discovered sites, asserted witness cells vs state visits, compact traces, and
// unresolved-edge closure.
func (l *InteractionLedger) RenderMarkdown() string {
	l.ReconcileUnresolvedEdges()
	var b strings.Builder
	fmt.Fprintf(&b, "# Source-Derived Interaction Ledger\n\n")
	fmt.Fprintf(&b, "- **SSA producer graph stats**: `root_packages=%d`, `in_scope_packages=%d`, `indexed_functions=%d`, `default_graph_builds=%d`\n",
		l.ProducerGraphStats.RootPackageCount, l.ProducerGraphStats.InScopePackageCount,
		l.ProducerGraphStats.IndexedFunctionCount, l.ProducerGraphStats.DefaultGraphBuildCount)
	fmt.Fprintf(&b, "- **Total discovered actionable sites**: `%d`\n", l.TotalDiscoveredSites)
	fams := make([]string, 0, len(l.SitesByFamily))
	for k := range l.SitesByFamily {
		fams = append(fams, k)
	}
	slices.Sort(fams)
	for _, f := range fams {
		fmt.Fprintf(&b, "  - `%s`: `%d`\n", f, l.SitesByFamily[f])
	}
	fmt.Fprintf(&b, "- **Unexplained witness gaps**: `%d`\n", len(l.UnexplainedGaps))
	fmt.Fprintf(&b, "- **Unresolved producer edges**: `%d` (`%d` required scopes, `%d` closed by executed assertions, `%d` closed by source-grounded exclusions, `%d` open gaps)\n\n",
		len(l.UnresolvedEdges), len(l.RequiredEdgeScopes), len(l.ExecutedEdgeAssertions), len(l.ApprovedEdgeExclusions), len(l.OpenEdgeGaps))

	b.WriteString("## Paired & Multi-Way Witness Summary\n\n")
	b.WriteString("| Witness | Source Span | Action | State Visits | Deduplicated Projections | Realizable Cells | Independently Asserted | Unasserted | Exclusions |\n")
	b.WriteString("|---|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range l.WitnessRows {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d | %d | %d | %d | %d | %d |\n",
			r.WitnessID, r.SourceSpan, r.AffectedAction, r.StateVisits, r.DeduplicatedProjections,
			len(r.RealizableRelevantCells), len(r.IndependentlyAsserted), len(r.UnassertedRealizableCells), len(r.Exclusions))
	}

	b.WriteString("\n## Compact Source-to-Primitive Traces\n\n")
	for _, ct := range l.CompactTraces {
		fmt.Fprintf(&b, "### `%s` (`%s` -> `%s`)\n", ct.Name, ct.ContributionID, ct.DeliverySite)
		fmt.Fprintf(&b, "- **Actions**: `%s`\n", strings.Join(ct.Actions, "`, `"))
		fmt.Fprintf(&b, "- **Capability facts**:\n")
		for _, cf := range ct.CapabilityFacts {
			fmt.Fprintf(&b, "  - `%s` (producers: `%s`) -> inputs: `%s`\n",
				cf.Fact, strings.Join(cf.ProducerSites, ", "), strings.Join(cf.PrimitiveInputs, ", "))
		}
		fmt.Fprintf(&b, "- **Occurrence facts**:\n")
		for _, of := range ct.OccurrenceFacts {
			fmt.Fprintf(&b, "  - `%s` (producers: `%s`) -> inputs: `%s`\n",
				of.Fact, strings.Join(of.ProducerSites, ", "), strings.Join(of.PrimitiveInputs, ", "))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Unresolved Producer Edges & Closure Join\n\n")
	b.WriteString("| Edge ID | Producer Location | Reason |\n")
	b.WriteString("|---|---|---|\n")
	for _, u := range l.UnresolvedEdges {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n", u.EdgeID, u.ProducerLocation, u.Reason)
	}
	return b.String()
}
