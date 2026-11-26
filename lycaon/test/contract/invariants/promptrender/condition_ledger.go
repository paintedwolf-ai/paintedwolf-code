package promptrender

import (
	"context"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/internal/oarproducers"
)

const unscopedRecipient = oarproducers.RecipientUnscoped

// renderRecipient names a render's recipient by its reader's is_coordinator
// fact; a render with no reader is unscoped.
func renderRecipient(scope map[string]any) string {
	switch v, ok := scope["is_coordinator"].(bool); {
	case !ok:
		return unscopedRecipient
	case v:
		return interactions.RecipientCoordinator
	default:
		return interactions.RecipientWorker
	}
}

// occurrenceRecipient names an OAR occurrence's recipient by the session role
// its producer sets.
func occurrenceRecipient(gc *oar.GuardContext) string {
	if gc == nil || gc.Session.Role == "" {
		return unscopedRecipient
	}
	return gc.Session.Role
}

// conditionConstraints are the production invariants that hold guard atoms
// constant or coupled on a recipient.
var conditionConstraints = []interactions.AtomConstraint{
	{Atom: "is_coordinator", Recipient: interactions.RecipientCoordinator, Fixed: true, Value: independence.True,
		Source: "test/contract/invariants/promptrender/condition_ledger.go:renderRecipient partitions renders by is_coordinator"},
	{Atom: "is_coordinator", Recipient: interactions.RecipientWorker, Fixed: true, Value: independence.False,
		Source: "test/contract/invariants/promptrender/condition_ledger.go:renderRecipient partitions renders by is_coordinator"},
	{Atom: "has_git", Implies: "has_any_git_root", Recipient: interactions.RecipientCoordinator, Source: srcGitFacts},
	{Atom: "has_git", Implies: "has_any_git_root", Recipient: interactions.RecipientWorker, Source: srcGitFacts},
	{Atom: "paintedwolf.command_loadable_without_schema", Implies: admittedProcessFieldsEmpty, Recipient: interactions.RecipientCoordinator, Source: srcLoaderOnly},
	{Atom: "paintedwolf.command_loadable_without_schema", Implies: admittedProcessFieldsEmpty, Recipient: interactions.RecipientWorker, Source: srcLoaderOnly},
}

const (
	admittedProcessFieldsEmpty = "size(paintedwolf.admitted_process_capability_fields) == 0"
	srcLoaderOnly              = "internal/tools/recovery_form_facts.go:registerCommandFormRecoveryFacts admits capability fields only when command accepts its command field, which a loader-only command without a schema does not"
)

const srcGitFacts = "internal/session/readerfacts/resolver.go:gitFacts starts hasAnyGit at hasGit, so the any-root fact includes the primary root"

// occurrenceRoleFacts are the session facts each recipient's occurrences
// carry: occurrenceRecipient partitions by session_role, and every producer
// that names a role names the worker exactly for a worker leg. An unscoped
// occurrence is a worker leg through policyfacts or one without a session.
var occurrenceRoleFacts = map[string]map[string]any{
	interactions.RecipientCoordinator: {"paintedwolf.session_role": "coordinator", "paintedwolf.worker_leg": false},
	interactions.RecipientWorker:      {"paintedwolf.session_role": "worker", "paintedwolf.worker_leg": true},
	unscopedRecipient:                 {"paintedwolf.session_role": ""},
}

const (
	srcOccurrenceRole = "occurrenceRecipient partitions occurrences by session_role; toolfeedback/block_plane.go:fillOccurrenceIdentity, " +
		"session/policyfacts/service.go:FillSessionFacts (coordinator only), session/workercompletion/worker_summary.go and " +
		"session/workeroutcomes/delivery_policy.go set the worker role exactly with worker_leg"
	srcConfine = "internal/session/policyfacts/oar_observe.go:ObserveConfine publishes execution facts only for an applied confinement"
)

// caseBudget bounds targeted generation: runs per gap and in total.
var caseBudget = casegen.Budget{PerTarget: 120, Total: 12000}

// StockConditionLedger discovers every stock guard, joins it with the atom
// observations of the host-state matrix, then generates targeted OAR cases
// for the gaps through the production hook producers and joins again.
func StockConditionLedger(ctx context.Context, s *Stock, extra ...[]hoststate.ObservedCondition) (*interactions.ConditionLedger, error) {
	sum, err := RunCartesianMatrix(ctx, s)
	if err != nil {
		return nil, err
	}
	sites, err := interactions.DiscoverConditionSites(s.Catalog, s.Schemas)
	if err != nil {
		return nil, err
	}
	gen, err := newOARCaseGenerator(ctx, s)
	if err != nil {
		return nil, err
	}
	in := interactions.LedgerInput{
		Sites: sites, Observed: interactions.MergeObservations(append([][]hoststate.ObservedCondition{sum.Conditions}, extra...)...),
		Constraints: conditionConstraints, Derived: gen.derived,
	}
	first, err := interactions.BuildConditionLedger(in)
	if err != nil {
		return nil, err
	}
	targets, gapRecipients := gen.targets(first)
	started := time.Now()
	outcomes, err := casegen.Generate(ctx, gen.producer, targets, caseBudget)
	if err != nil {
		return nil, err
	}
	in.Observed = interactions.MergeObservations(in.Observed, gen.producer.Observations())
	in.Searches = searchesOf(outcomes, gapRecipients)
	guided, err := generateGuidanceCases(ctx, s, first, &in)
	if err != nil {
		return nil, err
	}
	l, err := interactions.BuildConditionLedger(in)
	if err != nil {
		return nil, err
	}
	l.Generation = interactions.GenerationSummary{Targets: len(targets), Runs: gen.producer.Runs(), Seconds: time.Since(started).Seconds()}
	for _, o := range outcomes {
		if o.Found {
			l.Generation.Found++
		}
	}
	addGuidanceGeneration(&l.Generation, guided)
	return l, nil
}

// NewConditionTracker returns a tracker that attributes stock template and
// OAR guard evaluations to the ledger's sites and recipients.
func NewConditionTracker(s *Stock) (*hoststate.CoverageTracker, error) {
	tracker := hoststate.NewCoverageTracker()
	if err := registerStockTemplatesInTracker(s, tracker); err != nil {
		return nil, err
	}
	tracker.ObserveConditions(renderRecipient, occurrenceRecipient)
	return tracker, nil
}

type oarCaseGenerator struct {
	producer *oarproducers.CaseProducer
}

func newOARCaseGenerator(ctx context.Context, s *Stock) (*oarCaseGenerator, error) {
	coordStates, err := hoststate.CoordinatorStates(ctx)
	if err != nil {
		return nil, err
	}
	workerStates, err := hoststate.WorkerStates(ctx)
	if err != nil {
		return nil, err
	}
	cells, err := buildOARAuthorityCells(ctx, s, coordStates, workerStates)
	if err != nil {
		return nil, err
	}
	harness, err := oarproducers.NewHarness(s.Catalog, s.Schemas, s.Engine())
	if err != nil {
		return nil, err
	}
	p, err := oarproducers.NewCaseProducer(harness, cells, occurrenceRecipient)
	if err != nil {
		return nil, err
	}
	return &oarCaseGenerator{producer: p}, nil
}

// derived returns what an OAR guard's own expression and its producer fix:
// atoms reading one fact can only take the truths that fact's values
// realise, the recipient fixes the session role facts, confinement facts
// stay zero unless confinement applied, shared temp guards run only on a
// refusal, a tool selector fixes the tool-derived facts, and a block rule
// evaluated earlier hides the vectors under which it fires.
func (g *oarCaseGenerator) derived(site interactions.ConditionSite, recipient string) independence.Constraints {
	var c independence.Constraints
	if site.Family != "oar" {
		return guidanceDerived(site, recipient)
	}
	logic := g.producer.Logic(site.ID)
	if logic == nil {
		return c
	}
	c.Joints = append(casegen.SharedFactJoints(site.Atoms, logic), g.producer.ShadowJoints(site.ID, site.Atoms)...)
	if fixed, ok := occurrenceRoleFacts[recipient]; ok {
		c.Fixed = casegen.FixedFacts(site.Atoms, logic, fixed, srcOccurrenceRole)
	}
	switch g.producer.Anchor(site.ID) {
	case oar.AnchorToolPost:
		c.Joints = append(c.Joints, casegen.ZeroUnless(site.Atoms, logic, "paintedwolf.confine_applied", oarproducers.ConfineFacts(), srcConfine)...)
	case oar.AnchorSharedTemp:
		if slices.Contains(site.Atoms, sharedTempRefusedAtom) {
			c.Fixed = append(c.Fixed, independence.Fixed{Atom: sharedTempRefusedAtom, Value: independence.True, Source: srcSharedTemp})
		}
	}
	c.Fixed = append(c.Fixed, g.producer.SelectorFixed(site.ID, site.Atoms)...)
	return c
}

const (
	sharedTempRefusedAtom = "size(paintedwolf.refused_shared_temp) > 0"
	srcSharedTemp         = "internal/session/policyfacts/oar_block.go:AfterTool evaluates sandbox.shared_temp only when the settlement refused a shared temp root"
)

// targets turns each OAR gap into generation targets: a guard nobody
// evaluated is searched on every recipient its producer reaches.
func (g *oarCaseGenerator) targets(l *interactions.ConditionLedger) ([]casegen.Target, []string) {
	byID := map[string]interactions.ConditionSite{}
	for _, s := range l.Sites {
		byID[s.ID] = s
	}
	var out []casegen.Target
	var gapRecipients []string
	for _, gap := range l.Gaps {
		site := byID[gap.Site]
		logic := g.producer.Logic(gap.Site)
		if site.Family != "oar" || logic == nil {
			continue
		}
		needs := gap.Needs
		if len(needs) == 0 {
			if decide := g.producer.Decide(gap.Site); decide != nil {
				needs = casegen.Needs(distinctAtoms(site.Atoms), gap.Atom, decide, 16)
			}
		}
		recipients := []string{gap.Recipient}
		if gap.Recipient == interactions.RecipientAny {
			recipients = []string{interactions.RecipientCoordinator, interactions.RecipientWorker, unscopedRecipient}
		}
		for _, r := range recipients {
			out = append(out, casegen.Target{Site: gap.Site, Recipient: r, Input: gap.Atom, Needs: needs, References: logic.References})
			gapRecipients = append(gapRecipients, gap.Recipient)
		}
	}
	return out, gapRecipients
}

// searchesOf keys each unwitnessed outcome's reason by its gap; a gap
// searched on several recipients lists each.
func searchesOf(outcomes []casegen.Outcome, gapRecipients []string) map[string]string {
	out := map[string]string{}
	for i, o := range outcomes {
		if o.Found {
			continue
		}
		key := interactions.SearchKey(o.Site, gapRecipients[i], o.Input)
		reason := o.Reason
		if gapRecipients[i] != o.Recipient {
			reason = o.Recipient + ": " + reason
		}
		if prev := out[key]; prev != "" {
			reason = prev + "; " + reason
		}
		out[key] = reason
	}
	return out
}

func distinctAtoms(atoms []string) []string {
	var out []string
	for _, a := range atoms {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}
