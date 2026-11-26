package oarproducers

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// Recipients a generated occurrence is delivered to, named as the ledger's
// occurrence partition names them. policyfacts.FillSessionFacts leaves a
// worker leg's session_role empty, so policyfacts occurrences of worker
// sessions are unscoped, like block-plane occurrences without a session.
const (
	RecipientCoordinator = "coordinator"
	RecipientWorker      = "worker"
	RecipientUnscoped    = "unscoped"
)

type entryKind int

const (
	entryRejected entryKind = iota
	entryBlock
	entryAfterTool
	entryCredential
)

// caseEntry is the production hook that delivers an anchor's occurrences and
// the recipients its production callers reach.
type caseEntry struct {
	kind       entryKind
	recipients []string
	tool       string
}

// caseEntries maps each anchor to its producer: tool.rejected through the
// block plane, a turn checkpoint through policyfacts.Service.Block, tool
// results through AfterTool. Recipients follow toolvocab's anchor audience
// and the hook callers: coordinator checkpoints have coordinator sessions;
// the block plane names a worker leg worker and runs without a session too;
// policyfacts leaves a worker leg unscoped; worker summaries are evaluated
// with the worker role (workercompletion.EvaluateWorkerSummary).
var caseEntries = map[string]caseEntry{
	oar.AnchorToolRejected:             {entryRejected, []string{RecipientCoordinator, RecipientWorker, RecipientUnscoped}, "command"},
	oar.AnchorToolPost:                 {entryAfterTool, []string{RecipientCoordinator, RecipientUnscoped}, "command"},
	oar.AnchorSharedTemp:               {entryAfterTool, []string{RecipientCoordinator, RecipientUnscoped}, "command"},
	oar.AnchorCredentialAssignment:     {entryCredential, []string{RecipientCoordinator, RecipientUnscoped}, "write"},
	oar.AnchorSessionPreInvoke:         {entryBlock, []string{RecipientCoordinator, RecipientUnscoped}, "task"},
	oar.AnchorCoordinatorPreInvoke:     {entryBlock, []string{RecipientCoordinator}, "verify"},
	oar.AnchorCoordinatorCloseoutCheck: {entryBlock, []string{RecipientCoordinator}, ""},
	oar.AnchorCoordinatorMidTurnNote:   {entryBlock, []string{RecipientCoordinator}, ""},
	oar.AnchorCoordinatorPostTurn:      {entryBlock, []string{RecipientCoordinator}, ""},
	oar.AnchorWorkerReportCheck:        {entryBlock, []string{RecipientWorker, RecipientUnscoped}, "complete_leg"},
}

// producerFacts are facts the producer itself establishes from the session,
// the invocation or the rejection (GuardContext.DeriveToolClassFacts derives
// the tool classes); they move only through the cell, tool and code inputs.
var producerFacts = map[string]bool{
	"paintedwolf.operation": true, "paintedwolf.rejection_code": true, "paintedwolf.session_role": true,
	"paintedwolf.session_mode": true, "paintedwolf.session_posture": true, "paintedwolf.surface": true,
	"paintedwolf.worker_leg": true, "paintedwolf.anchor": true, "paintedwolf.tool_profile": true,
	"paintedwolf.principal": true, "paintedwolf.principal_roles": true, "paintedwolf.credential_decisions": true,
	"paintedwolf.command_form": true, "paintedwolf.native_operation": true,
	"paintedwolf.tool_is_task": true, "paintedwolf.tool_is_state": true, "paintedwolf.tool_is_delegation": true,
	"paintedwolf.tool_is_handoff": true, "paintedwolf.tool_payload_chunkable": true, "paintedwolf.receiving_tool_summarize": true,
}

// confineFacts are the execution facts ObserveConfine publishes from a
// confined invocation's observation.
var confineFacts = []string{
	"paintedwolf.network_mode", "paintedwolf.denial_subject", "paintedwolf.confine_signals",
	"paintedwolf.failed_stages", "paintedwolf.process_running",
	"paintedwolf.refused_credential_store_paths", "paintedwolf.refused_socket_paths",
	"paintedwolf.refused_connect_ports", "paintedwolf.refused_listen_ports", "paintedwolf.refused_signals",
	"paintedwolf.unsandboxed_refusals", "paintedwolf.refused_terminal_write_paths",
	"paintedwolf.refused_write_paths", "paintedwolf.ordinary_write_paths", "paintedwolf.refused_terminal_read_paths",
	"paintedwolf.refused_read_paths", "paintedwolf.refused_write_grants", "paintedwolf.ordinary_write_grants",
	"paintedwolf.refused_read_grants", "paintedwolf.refused_multiple_read_grants",
}

// ConfineFacts lists the facts ObserveConfine publishes only for a confined
// invocation.
func ConfineFacts() []string { return slices.Clone(confineFacts) }

// sandboxRefusalSets are kernel refusal observations, one per recovery the
// post-invoke rules distinguish.
var sandboxRefusalSets = [][]confine.SandboxRefusal{
	{{Operation: "file-write-create", Target: "/opt/x", Process: "touch", Count: 1, Recovery: confine.RecoverWriteRoot, Grant: "/opt", Layer: confine.FloorOutsideWriteRoots}},
	{{Operation: "file-read-data", Target: "/opt/r", Process: "cat", Count: 1, Recovery: confine.RecoverReadPath, Grant: "/opt", Layer: confine.FloorReadConfigured}},
	{{Operation: "file-read-data", Target: "/home/u/.ssh/id_rsa", Process: "cat", Count: 1, Recovery: confine.RecoverCredentialStore, Grant: "/home/u/.ssh/id_rsa", Layer: confine.FloorReadCredentialStore}},
	{{Operation: "host-service", Target: "docker", Process: "docker", Count: 1, Recovery: confine.RecoverHostExecution}},
	{{Operation: "net-connect", Target: "remote:127.0.0.1:5432", Process: "nc", Count: 1, Recovery: confine.RecoverOutboundPort}},
	{{Operation: "net-bind", Target: "local:127.0.0.1:8080", Process: "node", Count: 1, Recovery: confine.RecoverLocalListen}},
	{{Operation: "signal", Target: "SIGTERM", Process: "kill", Count: 1, Recovery: confine.RecoverProcessControl}},
	{{Operation: "unix-connect", Target: "/var/run/docker.sock", Process: "socat", Count: 1, Recovery: confine.RecoverSocketPath, Grant: "/var/run/docker.sock"}},
	{
		{Operation: "file-write-create", Target: "/opt/x", Process: "touch", Count: 1, Recovery: confine.RecoverWriteRoot, Grant: "/opt", Layer: confine.FloorOutsideWriteRoots},
		{Operation: "file-read-data", Target: "/opt/r", Process: "cat", Count: 1, Recovery: confine.RecoverReadPath, Grant: "/opt", Layer: confine.FloorReadConfigured},
	},
	{{Operation: "file-read-data", Target: "/etc/pw-ctrl", Process: "cat", Count: 1, Recovery: confine.RecoverNone, Layer: confine.FloorReadControlPlane}},
}

// generatedRefusalSets add the control-plane write refusal, whose path is
// terminal, to the matrix's refusal observations.
var generatedRefusalSets = append(slices.Clone(sandboxRefusalSets),
	[]confine.SandboxRefusal{{Operation: "file-write-data", Target: "/state/approvals.yaml", Process: "tee", Count: 1, Recovery: confine.RecoverNone, Layer: confine.FloorControlPlane}},
)

// credentialValues are assigned literals whose secretmint measurements
// differ in length, word count, character runs and listing.
var credentialValues = []string{
	"alpha_bravo_charlie_delta_123456",
	"password1",
	"Zq8vN2kL5mX9pR4tW7yB",
	"aaaaaaaaaaaaaaaaaaaaaaaa",
	"correct-horse-battery-staple-9",
	"kX7mQ2pL9vR4",
	"alpha_bravo_charlie_x",
	"Q7xZ2mK9pL4vN8rT",
	"zebra_quartz_violet_mango",
	"0123456789abcdefABCDEF",
	"ab_cd_ef_gh",
	"k1-k2-k3-k4-k5",
	"Xq7Lm2Pz9Kd4Rt8Vw3Nb_alpha_bravo_charlie",
	"1234qwerasdfzxcv",
	"q1w2e3r4t5y6u7i8o9p0",
}

// CaseProducer runs generated occurrences through the production hook
// producers the harness wires, one fresh session per case, and keeps every
// guard evaluation the production evaluator recorded.
type CaseProducer struct {
	h         *Harness
	env       *oarcore.Environment
	cells     map[string][]AuthCell
	partition func(*oar.GuardContext) string
	rules     map[string]*oar.Rule
	altCodes  []string
	fields    map[string]*factField
	observed  map[string]*hoststate.ObservedCondition
	runs      int
	compiled  map[string]*oarcore.Rule
	shadows   map[string][]independence.Joint
}

// NewCaseProducer draws each recipient's states from cells: the first
// coordinator and worker cells offering command are the baselines.
func NewCaseProducer(h *Harness, cells []AuthCell, partition func(*oar.GuardContext) string) (*CaseProducer, error) {
	env, err := oar.InstalledEngineDeclaration().Environment()
	if err != nil {
		return nil, err
	}
	p := &CaseProducer{
		h: h, env: env, partition: partition, cells: map[string][]AuthCell{},
		rules: map[string]*oar.Rule{}, fields: map[string]*factField{}, observed: map[string]*hoststate.ObservedCondition{},
		compiled: map[string]*oarcore.Rule{}, shadows: map[string][]independence.Joint{},
	}
	for _, role := range []string{RecipientCoordinator, RecipientWorker} {
		p.cells[role] = recipientCells(cells, role)
		if len(p.cells[role]) == 0 {
			return nil, fmt.Errorf("no %s cell to generate cases from", role)
		}
	}
	p.cells[RecipientUnscoped] = p.cells[RecipientWorker]
	for _, r := range h.Rules.All() {
		if r != nil {
			p.rules[r.Qualified()] = r
		}
	}
	// The pipeline evaluates an anchor's rules in this order and the first
	// block stops it, so alternative codes come from the last rules evaluated.
	var rejected []*oar.Rule
	for _, r := range h.Rules.OnAnchor(oar.AnchorToolRejected) {
		if len(oarcore.ConjunctValues(r.When, "paintedwolf.rejection_code")) > 0 {
			rejected = append(rejected, r)
		}
	}
	if len(rejected) < 2 {
		return nil, fmt.Errorf("fewer than two tool.rejected rules name a rejection code")
	}
	for _, r := range rejected[len(rejected)-2:] {
		p.altCodes = append(p.altCodes, oarcore.ConjunctValues(r.When, "paintedwolf.rejection_code")[0])
	}
	return p, nil
}

// recipientCells keeps one cell per distinct mode, posture, surface and
// profile of role, those offering command first, then the boundary request
// shapes, at most twelve.
func recipientCells(cells []AuthCell, role string) []AuthCell {
	var out []AuthCell
	seen := map[string]bool{}
	for _, pass := range []struct{ command, boundary bool }{{true, false}, {false, false}, {true, true}, {false, true}} {
		for _, c := range cells {
			if c.Role != role || c.IsBoundary != pass.boundary || slices.Contains(c.Auth.Offered, "command") != pass.command {
				continue
			}
			key := fmt.Sprintf("%s|%s|%s|%s|%s", c.Mode, c.Posture, c.SurfaceID, c.ProfileID, c.Auth.Digest)
			if seen[key] || len(out) == 12 || (pass.boundary && len(out) == 0) {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

func (p *CaseProducer) ruleOf(site string) (*oar.Rule, string) {
	id, branch, ok := strings.Cut(site, "#")
	if !ok {
		return nil, ""
	}
	r := p.rules[id]
	if r == nil {
		return nil, ""
	}
	return r, guardSources(r)[branch]
}

// guardSources maps each guarded branch of r to its condition source, named
// as RuleConditionSites names them.
func guardSources(r *oar.Rule) map[string]string {
	out := map[string]string{"when": r.When}
	var walk func(prefix string, list []oar.NextStep)
	walk = func(prefix string, list []oar.NextStep) {
		for i, st := range list {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			if strings.TrimSpace(st.When) != "" {
				out[path+".when"] = st.When
			}
			walk(path+".or", st.Or)
		}
	}
	if r.NextSteps != nil {
		walk("steps", r.NextSteps.Steps)
	}
	return out
}

// literals lists the string literals source compares fact with.
func literals(fact string, sources ...string) []string {
	var out []string
	for _, src := range sources {
		cmps, _ := oarcore.ConditionFactComparisons(src)
		for _, c := range cmps {
			if c.Fact == fact && !slices.Contains(out, c.Value) {
				out = append(out, c.Value)
			}
		}
	}
	return out
}

func (p *CaseProducer) references(sources ...string) []string {
	var out []string
	for _, src := range sources {
		refs, _ := oarcore.ConditionReferences(src)
		for _, r := range refs {
			if _, ok := p.env.Facts[r]; ok && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Knobs lists the producer inputs that can move site's facts on recipient:
// the invoked tool, the recipient's state, and each typed fact the guard or
// its rule's condition reads, through the input the anchor's producer takes.
func (p *CaseProducer) Knobs(site, recipient string) []casegen.Knob {
	rule, src := p.ruleOf(site)
	if rule == nil {
		return nil
	}
	entry, ok := caseEntries[rule.Anchor]
	if !ok || !slices.Contains(entry.recipients, recipient) {
		return nil
	}
	sources := []string{src, rule.When}
	knobs := []casegen.Knob{p.toolKnob(rule, entry.tool, sources), p.cellKnob(recipient)}
	if recipient != RecipientWorker {
		knobs = append(knobs, p.surfaceKnob(sources))
	}
	knobs = append(knobs, modeKnob(p.cells[recipient][0].Mode))
	switch entry.kind {
	case entryRejected:
		knobs = append(knobs, p.codeKnob(rule, sources))
		knobs = append(knobs,
			casegen.Knob{Name: "doom.count", Facts: []string{"paintedwolf.repeat_count", "paintedwolf.count", "paintedwolf.deferred_unactivated"}, Values: []any{0, 1, 9}},
			casegen.Knob{Name: "doom.code", Facts: []string{"paintedwolf.repeated_rejection_code"}, Values: []any{"", "TOOL_ARGS_INVALID"}},
		)
		if recipient == RecipientUnscoped {
			knobs = append(knobs, casegen.Knob{Name: "session", Facts: []string{casegen.AnyFact}, Values: []any{true, false}, Labels: []string{"", "none"}})
		}
		sample := defaultOccurrenceSample(rule)
		for _, f := range p.references(sources...) {
			if k, ok := p.factKnob("data.", f, sources, sample[strings.TrimPrefix(f, "paintedwolf.")]); ok {
				knobs = append(knobs, k)
			}
		}
	case entryBlock:
		for _, f := range p.references(sources...) {
			prefix := "data."
			if p.field(f) != nil {
				prefix = "gc."
			}
			if k, ok := p.factKnob(prefix, f, sources, nil); ok {
				knobs = append(knobs, k)
			}
		}
	case entryAfterTool:
		knobs = append(knobs, confineKnobs(sources)...)
		knobs = append(knobs, sharedTempKnob(),
			casegen.Knob{Name: "count", Facts: []string{"paintedwolf.repeat_count", casegen.AnyFact}, Values: []any{1, 4, 9}},
			casegen.Knob{
				Name: "args", Facts: []string{"tool_args", "paintedwolf.handle", "paintedwolf.query"},
				Values: []any{map[string]any{"command": "run"}, map[string]any{"handle": "cmd-1"}, map[string]any{"query": "x"}},
				Labels: []string{"command", "handle", "query"},
			})
	case entryCredential:
		values := make([]any, len(credentialValues))
		for i, v := range credentialValues {
			values[i] = v
		}
		credFacts := []string{"paintedwolf.credential_length", "paintedwolf.credential_distinct_words", "paintedwolf.credential_longest_run", "paintedwolf.credential_run_classes", "paintedwolf.listed"}
		knobs = append(knobs,
			casegen.Knob{Name: "content", Facts: credFacts, Values: values},
			casegen.Knob{Name: "harvested", Facts: []string{"paintedwolf.credential_harvested"}, Values: []any{false, true}},
			casegen.Knob{Name: "ignored", Facts: []string{"paintedwolf.credential_ignored"}, Values: []any{false, true}},
			casegen.Knob{Name: "repeat", Facts: []string{"paintedwolf.credential_decisions"}, Values: []any{1, 2}},
		)
	}
	return knobs
}

func (p *CaseProducer) toolKnob(rule *oar.Rule, fallback string, sources []string) casegen.Knob {
	var values []any
	add := func(names ...string) {
		for _, n := range names {
			if n != "" && !slices.Contains(values, any(n)) && len(values) < 12 {
				values = append(values, n)
			}
		}
	}
	firstOf := func(names []string) []string { return names[:min(len(names), 1)] }
	add(firstOf(rule.Tools())...)
	add(firstOf(oarcore.ConjunctValues(rule.When, "tool"))...)
	add(firstOf(rule.Operations())...)
	add(fallback, "command", "recall", "task", "verify")
	add(rule.Tools()...)
	add(literals("tool", sources...)...)
	add(literals("paintedwolf.operation", sources...)...)
	add("read", "write")
	return casegen.Knob{Name: "tool", Facts: []string{"tool", "paintedwolf.operation", casegen.AnyFact}, Values: values}
}

func (p *CaseProducer) codeKnob(rule *oar.Rule, sources []string) casegen.Knob {
	var values []any
	for _, c := range slices.Concat(oarcore.ConjunctValues(rule.When, "paintedwolf.rejection_code"), literals("paintedwolf.rejection_code", sources...), p.altCodes, p.otherOperationCode(rule)) {
		if !slices.Contains(values, any(c)) {
			values = append(values, c)
		}
	}
	return casegen.Knob{Name: "code", Facts: []string{"paintedwolf.rejection_code", casegen.AnyFact}, Values: values}
}

// otherOperationCode is the code of the first tool.rejected rule that
// requires operations rule never selects: delivered for rule's operation,
// that rule cannot fire before it.
func (p *CaseProducer) otherOperationCode(rule *oar.Rule) []string {
	own := slices.Concat(rule.Operations(), rule.Tools())
	for _, r := range p.h.Rules.OnAnchor(oar.AnchorToolRejected) {
		ops, codes := r.Operations(), oarcore.ConjunctValues(r.When, "paintedwolf.rejection_code")
		if r != rule && len(ops) > 0 && len(codes) > 0 && len(own) > 0 && !slices.ContainsFunc(ops, func(o string) bool { return slices.Contains(own, o) }) {
			return codes[:1]
		}
	}
	return nil
}

func (p *CaseProducer) cellKnob(recipient string) casegen.Knob {
	cells := p.cells[recipient]
	k := casegen.Knob{Name: "cell", Facts: []string{"paintedwolf.session_mode", "paintedwolf.session_posture", "paintedwolf.tool_profile", casegen.AnyFact}}
	for i, c := range cells {
		k.Values = append(k.Values, i)
		k.Labels = append(k.Labels, c.Label)
	}
	return k
}

func (p *CaseProducer) surfaceKnob(sources []string) casegen.Knob {
	values := []any{p.cells[RecipientCoordinator][0].SurfaceID}
	for _, s := range slices.Concat(literals("paintedwolf.surface", sources...), cellSurfaces(p.cells[RecipientCoordinator])) {
		if !slices.Contains(values, any(s)) {
			values = append(values, s)
		}
	}
	return casegen.Knob{Name: "surface", Facts: []string{"paintedwolf.surface"}, Values: values}
}

func cellSurfaces(cells []AuthCell) []string {
	var out []string
	for _, c := range cells {
		out = append(out, c.SurfaceID)
	}
	return out
}

func modeKnob(base api.SessionMode) casegen.Knob {
	values := []any{base}
	for _, m := range api.AllSessionModeValues() {
		if m != base {
			values = append(values, m)
		}
	}
	return casegen.Knob{Name: "mode", Facts: []string{"paintedwolf.session_mode"}, Values: values}
}

// factKnob is a typed input for fact: occurrence data or the GuardContext
// field its producer sets. Producer-established facts have none.
func (p *CaseProducer) factKnob(prefix, fact string, sources []string, base any) (casegen.Knob, bool) {
	if producerFacts[fact] || !strings.HasPrefix(fact, "paintedwolf.") {
		return casegen.Knob{}, false
	}
	lits := literals(fact, sources...)
	var values []any
	switch p.env.Facts[fact].Type {
	case oarcore.TypeBool:
		values = []any{false, true}
	case oarcore.TypeString:
		values = []any{""}
		for _, l := range lits {
			values = append(values, l)
		}
		values = append(values, "fixture-1")
	case oarcore.TypeInt:
		values = []any{int64(0), int64(1), int64(2), int64(3), int64(4), int64(9), int64(16), int64(20), int64(32)}
	case oarcore.TypeListString:
		values = []any{[]string{}, []string{"item-1"}}
		for _, l := range lits {
			values = append(values, []string{l})
		}
		if len(lits) > 1 {
			values = append(values, lits)
		}
	default:
		return casegen.Knob{}, false
	}
	if base != nil {
		if i, ok := base.(int); ok {
			base = int64(i)
		}
		values = slices.DeleteFunc(values, func(v any) bool { return fmt.Sprint(v) == fmt.Sprint(base) })
		values = append([]any{base}, values...)
	}
	return casegen.Knob{Name: prefix + strings.TrimPrefix(fact, "paintedwolf."), Facts: []string{fact}, Values: values}, true
}

// field returns the GuardContext field behind fact, discovering it once.
func (p *CaseProducer) field(fact string) *factField {
	if f, ok := p.fields[fact]; ok {
		return f
	}
	var out *factField
	if f, ok := discoverFactField(fact, p.env.Facts[fact].Type); ok {
		out = &f
	}
	p.fields[fact] = out
	return out
}

func confineKnobs(sources []string) []casegen.Knob {
	signals := []any{[]string{}}
	lits := literals("paintedwolf.confine_signals", sources...)
	for mask := 1; mask < 1<<len(lits) && len(lits) <= 4; mask++ {
		var set []string
		for i, l := range lits {
			if mask&(1<<i) != 0 {
				set = append(set, l)
			}
		}
		signals = append(signals, set)
	}
	denial := []any{""}
	for _, l := range literals("paintedwolf.denial_subject", sources...) {
		denial = append(denial, l)
	}
	knobs := []casegen.Knob{
		{Name: "confine.applied", Facts: confineFacts, Values: []any{true, false}},
		{Name: "confine.running", Facts: []string{"paintedwolf.process_running"}, Values: []any{false, true}},
		{Name: "confine.failed", Facts: []string{"paintedwolf.failed_stages"}, Values: []any{[]string{}, []string{"run"}}},
		{Name: "confine.signals", Facts: []string{"paintedwolf.confine_signals"}, Values: signals},
		{Name: "confine.denial", Facts: []string{"paintedwolf.denial_subject"}, Values: denial},
	}
	for i, set := range generatedRefusalSets {
		knobs = append(knobs, casegen.Knob{Name: fmt.Sprintf("refusal.%d", i), Facts: refusalFacts(set), Values: []any{false, true}})
	}
	return knobs
}

// refusalFacts names the facts observeSandboxRefusals publishes for set.
func refusalFacts(set []confine.SandboxRefusal) []string {
	var out []string
	for _, r := range set {
		switch r.Recovery {
		case confine.RecoverCredentialStore:
			out = append(out, "paintedwolf.refused_credential_store_paths")
		case confine.RecoverSocketPath:
			out = append(out, "paintedwolf.refused_socket_paths")
		case confine.RecoverOutboundPort:
			out = append(out, "paintedwolf.refused_connect_ports")
		case confine.RecoverLocalListen:
			out = append(out, "paintedwolf.refused_listen_ports")
		case confine.RecoverProcessControl:
			out = append(out, "paintedwolf.refused_signals")
		case confine.RecoverHostExecution:
			out = append(out, "paintedwolf.unsandboxed_refusals")
		case confine.RecoverWriteRoot, confine.RecoverReadPath, confine.RecoverNone:
			out = append(out, "paintedwolf.refused_write_paths", "paintedwolf.ordinary_write_paths", "paintedwolf.refused_write_grants",
				"paintedwolf.ordinary_write_grants", "paintedwolf.refused_read_paths", "paintedwolf.refused_read_grants",
				"paintedwolf.refused_multiple_read_grants", "paintedwolf.refused_terminal_read_paths", "paintedwolf.refused_terminal_write_paths")
		}
	}
	return out
}

func sharedTempKnob() casegen.Knob {
	refused := []string{"/tmp/shared-x"}
	return casegen.Knob{
		Name:  "shared_temp",
		Facts: []string{"paintedwolf.refused_shared_temp", "paintedwolf.shared_temp_tolerated", "paintedwolf.shared_temp_allowed", "paintedwolf.shared_temp_declined", "paintedwolf.shared_temp_unanswered", "paintedwolf.session_scratch_available"},
		Values: []any{
			(*guidance.SharedTempSettlement)(nil),
			&guidance.SharedTempSettlement{Refused: refused},
			&guidance.SharedTempSettlement{Refused: refused, Tolerated: true},
			&guidance.SharedTempSettlement{Refused: refused, Running: true, ScratchAvailable: true},
		},
		Labels: []string{"none", "refused", "tolerated", "running"},
	}
}

// occurrence is one generated case's producer inputs.
type occurrence struct {
	rule      *oar.Rule
	recipient string
	cell      AuthCell
	sessID    string
	tool      string
	code      string
	data      map[string]any
	fields    []fieldValue
	confine   confine.Observation
	refusals  []int
	doomCount int
	doomCode  string
	noSession bool
	shared    *guidance.SharedTempSettlement
	args      map[string]any
	count     int
	content   string
	harvested bool
	ignored   bool
	repeat    int
}

type fieldValue struct {
	f *factField
	v any
}

func (p *CaseProducer) occurrence(rule *oar.Rule, recipient string, knobs []casegen.Knob, s casegen.Setting) *occurrence {
	o := &occurrence{rule: rule, recipient: recipient, data: map[string]any{}, count: 1, repeat: 1, content: credentialValues[0], args: map[string]any{"command": "run"}}
	o.confine.Applied = true
	for _, k := range knobs {
		if k.Name == "cell" {
			o.cell = p.cells[recipient][s.Value(k).(int)]
		}
	}
	if rule.Anchor == oar.AnchorToolRejected {
		maps.Copy(o.data, defaultOccurrenceSample(rule))
	}
	for _, k := range knobs {
		v := s.Value(k)
		switch name := k.Name; {
		case name == "tool":
			o.tool = v.(string)
		case name == "code":
			o.code = v.(string)
		case name == "surface":
			o.cell.SurfaceID = v.(string)
		case name == "mode":
			o.cell.Mode = v.(api.SessionMode)
		case name == "confine.applied":
			o.confine.Applied = v.(bool)
		case name == "confine.running":
			o.confine.Running = v.(bool)
		case name == "confine.failed":
			o.confine.FailedStages = v.([]string)
		case name == "confine.signals":
			o.confine.Signals = v.([]string)
		case name == "confine.denial":
			o.confine.DenialSubject = v.(string)
		case strings.HasPrefix(name, "refusal."):
			if v.(bool) {
				i, _ := strconv.Atoi(strings.TrimPrefix(name, "refusal."))
				o.refusals = append(o.refusals, i)
			}
		case name == "doom.count":
			o.doomCount = v.(int)
		case name == "doom.code":
			o.doomCode = v.(string)
		case name == "session":
			o.noSession = !v.(bool)
		case name == "shared_temp":
			o.shared = v.(*guidance.SharedTempSettlement)
		case name == "count":
			o.count = v.(int)
		case name == "args":
			o.args = v.(map[string]any)
		case name == "content":
			o.content = v.(string)
		case name == "harvested":
			o.harvested = v.(bool)
		case name == "ignored":
			o.ignored = v.(bool)
		case name == "repeat":
			o.repeat = v.(int)
		case strings.HasPrefix(name, "data."):
			o.data[strings.TrimPrefix(name, "data.")] = v
		case strings.HasPrefix(name, "gc."):
			o.fields = append(o.fields, fieldValue{p.field("paintedwolf." + strings.TrimPrefix(name, "gc.")), v})
		}
	}
	for _, i := range o.refusals {
		o.confine.Refusals.Refusals = append(o.confine.Refusals.Refusals, generatedRefusalSets[i]...)
	}
	return o
}

// Run delivers one generated occurrence through the anchor's production
// producer with atom observation on and returns the recorded evaluations of
// site on recipient.
func (p *CaseProducer) Run(ctx context.Context, site, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]casegen.Vector, error) {
	rule, _ := p.ruleOf(site)
	if rule == nil {
		return nil, fmt.Errorf("no rule for %s", site)
	}
	entry := caseEntries[rule.Anchor]
	o := p.occurrence(rule, recipient, knobs, s)
	p.runs++
	o.sessID = fmt.Sprintf("gen-%d", p.runs)

	tracker := hoststate.NewCoverageTracker()
	tracker.ObserveConditions(func(map[string]any) string { return "" }, p.partition)
	tracker.SetCase(id)
	restore := tracker.Install()
	p.deliver(ctx, entry, o)
	restore()
	_ = p.h.takeAdvisories()
	p.h.resetExercisedEdges()

	var out []casegen.Vector
	for _, c := range tracker.Conditions() {
		if c.Family != "oar" {
			continue
		}
		p.merge(c)
		if c.Site != site {
			continue
		}
		for _, v := range c.Vectors {
			if v.Partition == recipient && v.Recomposes {
				out = append(out, vectorOf(c.Atoms, v))
			}
		}
	}
	return out, nil
}

func (p *CaseProducer) deliver(ctx context.Context, entry caseEntry, o *occurrence) {
	h := p.h
	var cctx context.Context
	var sess *api.Session
	if o.noSession {
		cctx = toolreach.WithRequest(ctx, o.cell.Reach)
	} else {
		h.bindSession(o.sessID, o.cell)
		cctx, sess = o.cell.Context(ctx, o.sessID), o.cell.Session(o.sessID)
	}
	switch entry.kind {
	case entryRejected:
		if o.doomCount > 0 || o.doomCode != "" {
			_, _ = h.Policy.FormatDoomLoopReject(cctx, o.sessID, o.tool, maps.Clone(o.data), o.doomCount, o.doomCode)
			break
		}
		_ = h.BlockPlane.RejectObservation(cctx, o.tool, o.cell.ProfileID, nil, &toolrejection.ToolReject{Code: o.code, Data: o.data})
	case entryBlock:
		_, _, _ = h.Policy.Block(cctx, o.rule.Anchor, sess, o.tool, nil, func(gc *oar.GuardContext) error {
			if o.rule.Anchor == oar.AnchorWorkerReportCheck && o.recipient == RecipientWorker {
				gc.Session.Role, gc.Session.WorkerLeg = RecipientWorker, true
			}
			for _, fv := range o.fields {
				if fv.f != nil {
					fv.f.set(gc, fv.v)
				}
			}
			if len(o.data) > 0 {
				if gc.ObservationData == nil {
					gc.ObservationData = map[string]any{}
				}
				maps.Copy(gc.ObservationData, o.data)
			}
			return nil
		})
	case entryAfterTool:
		_, _ = h.Policy.AfterTool(cctx, sess, o.tool, maps.Clone(o.args), "ok", o.count, guidance.ToolResultFacts{Confine: o.confine, SharedTemp: o.shared})
	case entryCredential:
		if o.harvested {
			for _, root := range []string{o.sessID, "coord-parent-" + o.sessID} {
				h.Harvest.Harvest(secretharvest.ContainerRead{RootSessionID: root, Container: ".env", Content: []byte("SERVICE_SECRET=" + o.content + "\n")})
			}
		}
		h.ignoreCredentials = o.ignored
		for range o.repeat {
			_, _ = h.Policy.AfterTool(cctx, sess, o.tool, map[string]any{"content": "API_TOKEN=" + o.content + "\n"}, "ok", 1, guidance.ToolResultFacts{})
		}
		h.ignoreCredentials = false
	}
}

func vectorOf(atoms []string, v hoststate.ObservedVector) casegen.Vector {
	out := casegen.Vector{Truths: map[string]independence.Truth{}, Decision: v.Decision, Evaluated: true}
	for i, a := range atoms {
		if i >= len(v.Atoms) {
			break
		}
		if prev, ok := out.Truths[a]; ok && prev != v.Atoms[i] {
			out.Truths[a] = independence.Unknown
			continue
		}
		out.Truths[a] = v.Atoms[i]
	}
	return out
}

func (p *CaseProducer) merge(c hoststate.ObservedCondition) {
	cur := p.observed[c.Site]
	if cur == nil {
		cp := c
		cp.Vectors = nil
		p.observed[c.Site], cur = &cp, &cp
	}
	for _, e := range c.Errors {
		if !slices.Contains(cur.Errors, e) {
			cur.Errors = append(cur.Errors, e)
		}
	}
	for _, v := range c.Vectors {
		if !slices.ContainsFunc(cur.Vectors, func(w hoststate.ObservedVector) bool {
			return w.Decision == v.Decision && w.Partition == v.Partition && slices.Equal(w.Atoms, v.Atoms)
		}) {
			cur.Vectors = append(cur.Vectors, v)
		}
	}
}

// Observations returns every OAR guard evaluation the generated cases
// produced, each distinct vector with the case that first produced it.
func (p *CaseProducer) Observations() []hoststate.ObservedCondition {
	out := make([]hoststate.ObservedCondition, 0, len(p.observed))
	for _, id := range slices.Sorted(maps.Keys(p.observed)) {
		out = append(out, *p.observed[id])
	}
	return out
}

// Runs counts the generated cases delivered.
func (p *CaseProducer) Runs() int { return p.runs }
