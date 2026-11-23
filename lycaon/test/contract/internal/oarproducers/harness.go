package oarproducers

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/configlayout"
	coordgrounding "github.com/lycaon/lycaon/internal/coordinator/grounding"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// AuthCell binds one frozen coordinator, worker, or boundary request authority
// to its production session/surface/profile metadata.
type AuthCell struct {
	Label      string
	Role       string
	SurfaceID  string
	ProfileID  string
	AgentType  string
	Mode       api.SessionMode
	Posture    api.SessionPosture
	Auth       hoststate.TurnAuthority
	Reach      toolreach.Request
	IsBoundary bool
}

// Context attaches the cell's frozen toolreach.Request and curationctx.Session.
func (c AuthCell) Context(base context.Context, sessionID string) context.Context {
	parentID := ""
	if c.Role == "worker" {
		parentID = "coord-parent-" + sessionID
	}
	ctx := toolreach.WithRequest(base, c.Reach)
	return curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       sessionID,
		ParentSessionID: parentID,
		Posture:         string(c.Posture),
		Mode:            string(c.Mode),
		Surface:         c.SurfaceID,
	})
}

// Session builds an *api.Session matching c's role, posture, and mode.
func (c AuthCell) Session(sessionID string) *api.Session {
	sess := &api.Session{
		ID:        sessionID,
		ProjectID: "proj-oar-prod",
		Posture:   c.Posture,
		Mode:      c.Mode,
		AgentType: c.AgentType,
	}
	if c.Role == "worker" {
		sess.ParentSessionID = "coord-parent-" + sessionID
		if sess.AgentType == "" {
			sess.AgentType = "implementer"
		}
	} else if sess.AgentType == "" {
		sess.AgentType = prompts.CoordinatorProfileID
	}
	return sess
}

// FiredOutcome records one rendered OAR policy outcome observed from a real
// production hook entry point along with its emitted next-step calls and
// dynamically observed indirect producer edges.
type FiredOutcome struct {
	Anchor         string
	RuleID         string
	EventLabel     string
	Cell           AuthCell
	FixCopy        string
	ActionCopy     string
	CheckClaims    bool
	NextCalls      []toolreach.Call
	Steps          []FiredStepOutcome
	ExercisedEdges []string
}

type noopNudgeFormatter struct{}

func (noopNudgeFormatter) FormatNudge(_ context.Context, code string, _ map[string]any) (string, error) {
	return code, nil
}

type surfaceMap struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *surfaceMap) PromptTurnSurfaceID(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[sessionID]
}

func (s *surfaceMap) Set(sessionID, surfaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sessionID] = surfaceID
}

type profileMap struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *profileMap) PromptToolProfile(_ context.Context, sess *api.Session) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sess != nil {
		if v := p.m[sess.ID]; v != "" {
			return v, nil
		}
		if sess.IsWorkerChild() {
			return "implement", nil
		}
	}
	return "coordinator", nil
}

func (p *profileMap) Set(sessionID, profileID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[sessionID] = profileID
}

type sessionStoreMap struct {
	chats.Sessions
	mu sync.Mutex
	m  map[string]*api.Session
}

func (s *sessionStoreMap) Get(_ context.Context, id string) (*api.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.m[id]; ok {
		return sess, nil
	}
	return nil, fmt.Errorf("session %q not found", id)
}

func (s *sessionStoreMap) Set(id string, sess *api.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = sess
}

type activeToolsStub struct{}

func (activeToolsStub) Active(string) map[string]bool { return nil }

type evidenceStoreStub struct{}

func (evidenceStoreStub) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

// Harness wires the effective catalog's OAR GuardPipeline to the production
// hook producers (BlockPlane, policyfacts.Service, turnguards.Service,
// secretharvest, coordgrounding.AmbientGroundingCoordinator, and workercompletion).
type Harness struct {
	ModuleRoot      string
	Loader          *oar.Loader
	Rules           *oar.RuleSet
	Pipeline        *oar.GuardPipeline
	Renderer        *oar.Renderer
	BlockPlane      *toolfeedback.BlockPlane
	Policy          *policyfacts.Service
	Feedback        *policyfeedback.Service
	TurnGuards      *turnguards.Service
	Harvest         *secretharvest.Runtime
	Ambient         *coordgrounding.AmbientGroundingCoordinator
	Sessions        *sessionStoreMap
	Transcripts     *transcriptStoreMap
	WorkerQueue     *workerQueueMap
	Surfaces        *surfaceMap
	Profiles        *profileMap
	FactTypes       map[string]string
	CapabilityFacts map[string]bool
	EvalCount       int
	advisoryMu      sync.Mutex
	advisories      []*oar.Decision
	edgeMu          sync.Mutex
	exercisedEdges  []string
}

// NewHarness constructs a production OAR hook harness for eff and schemas.
func NewHarness(eff *extpacks.EffectiveCatalog, schemas *toolschema.Config, engine prompts.PromptTemplateEngine) (*Harness, error) {
	moduleRoot := configlayout.FindModuleRoot()
	repoRoot := filepath.Dir(moduleRoot)
	schemaDir := filepath.Join(repoRoot, "schemas")
	if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, fmt.Errorf("install anchor catalog: %w", err)
	}
	if err := oar.InstallEngineDeclarationBundled(schemaDir); err != nil {
		return nil, fmt.Errorf("install OAR engine declaration: %w", err)
	}
	declPath := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
	decl, err := oar.LoadEngineDeclaration(declPath)
	if err != nil {
		return nil, fmt.Errorf("load OAR engine declaration: %w", err)
	}
	factTypes := make(map[string]string, len(decl.HostFacts))
	probeGC := oar.NewGuardContext()
	tools.RegisterRecoveryFacts(context.Background(), probeGC)
	for _, hf := range decl.HostFacts {
		factTypes[hf.Name] = hf.Type
		_ = probeGC.Ensure(hf.Name)
	}
	capFacts := map[string]bool{
		"command_operation_reachable": true,
		"admitted_command_forms":      true,
		"session_mode":                true,
		"session_posture":             true,
		"session_role":                true,
		"worker_leg":                  true,
	}
	for k := range probeGC.ObservationData {
		capFacts[k] = true
	}
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		return nil, fmt.Errorf("load hint config: %w", err)
	}
	if engine != nil {
		guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	}
	formatter := guidance.NewStaticRejectFormatter(cfg)
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("new OAR loader: %w", err)
	}
	loader.SetToolSchemas(schemas)
	loader.SetDetectors(oar.HostDetectors(nil))
	rs, err := loader.LoadEffectivePolicyWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("load effective OAR policy: %w", err)
	}
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.SetDetectors(oar.HostDetectors(nil))
	for _, r := range rs.All() {
		if r != nil && r.Anchor != "" {
			pipeline.EnableAnchor(r.Anchor)
		}
	}
	renderer := oar.NewRenderer(formatter, noopNudgeFormatter{})
	bp := &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: renderer}

	inspector, err := secretmint.LoadBundled()
	if err != nil {
		return nil, fmt.Errorf("load secretmint: %w", err)
	}
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x29}, 32))
	if err != nil {
		return nil, fmt.Errorf("new secret fingerprinter: %w", err)
	}
	harvest := secretharvest.NewRuntime(fp)
	sessions := &sessionStoreMap{m: map[string]*api.Session{}}
	transcripts := &transcriptStoreMap{m: map[string][]api.Message{}}
	workerQueue := &workerQueueMap{m: map[string][]api.WorkerTask{}}
	surfaces := &surfaceMap{m: map[string]string{}}
	profiles := &profileMap{m: map[string]string{}}
	fb := policyfeedback.New()
	fb.SetRenderer(renderer)

	policy := policyfacts.New(profiles)
	policy.SetSources(sessions, evidenceStoreStub{}, nil, fb, activeToolsStub{})
	policy.SetSurface(surfaces)
	policy.SetPipeline(pipeline)
	policy.SetCredentialSlotProvider(func(context.Context, *api.Session) *secretmint.Inspector { return inspector })
	policy.SetSecretFingerprinter(fp)

	h := &Harness{
		ModuleRoot:      moduleRoot,
		Loader:          loader,
		Rules:           rs,
		Pipeline:        pipeline,
		Renderer:        renderer,
		BlockPlane:      bp,
		Policy:          policy,
		Feedback:        fb,
		TurnGuards:      &turnguards.Service{ToolPolicy: policy, Feedback: fb},
		Harvest:         harvest,
		Sessions:        sessions,
		Transcripts:     transcripts,
		WorkerQueue:     workerQueue,
		Surfaces:        surfaces,
		Profiles:        profiles,
		FactTypes:       factTypes,
		CapabilityFacts: capFacts,
	}
	policy.SetIgnoredCredentialCandidate(func(_ context.Context, _, _ string) bool {
		h.recordExercisedEdges(ObserveIndirectProducerCall(moduleRoot, "paintedwolf.credential_ignored")...)
		return false
	})
	policy.SetHarvestedFingerprint(func(root string, sf secretmatch.SecretFingerprint) bool {
		h.recordExercisedEdges(ObserveIndirectProducerCall(moduleRoot, "paintedwolf.credential_harvested")...)
		return harvest.Has(root, sf)
	})
	h.wireAmbientGrounding()
	pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
		if d != nil {
			h.advisoryMu.Lock()
			h.advisories = append(h.advisories, d)
			h.advisoryMu.Unlock()
		}
		return nil
	})
	return h, nil
}

func (h *Harness) takeAdvisories() []*oar.Decision {
	h.advisoryMu.Lock()
	defer h.advisoryMu.Unlock()
	out := h.advisories
	h.advisories = nil
	return out
}

func (h *Harness) bindSession(sessID string, cell AuthCell) {
	h.Sessions.Set(sessID, cell.Session(sessID))
	h.Surfaces.Set(sessID, cell.SurfaceID)
	prof := cell.ProfileID
	if prof == "" {
		if cell.Role == "worker" {
			prof = "implement"
		} else {
			prof = "coordinator"
		}
	}
	h.Profiles.Set(sessID, prof)
}

func collectRuleTargetTools(rule *oar.Rule) []string {
	set := map[string]bool{}
	for _, t := range rule.Tools() {
		set[t] = true
	}
	for _, t := range oarcore.ConjunctValues(rule.When, "tool") {
		set[t] = true
	}
	for _, t := range oarcore.ConjunctValues(rule.When, "paintedwolf.operation") {
		set[t] = true
	}
	if rule.NextSteps != nil {
		var walk func([]oar.NextStep)
		walk = func(steps []oar.NextStep) {
			for _, st := range steps {
				if st.Tool != "" {
					set[st.Tool] = true
				}
				walk(st.Or)
			}
		}
		walk(rule.NextSteps.Steps)
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// selectCellsForRejectedRule retains all boundary cells and one cell per distinct
// OAR-observable capability signature for rule, plus rotating coverage across cells.
func selectCellsForRejectedRule(rule *oar.Rule, rIdx int, cells []AuthCell) []AuthCell {
	refs := collectNextStepConditionRefs(rule)
	if wRefs, err := oarcore.ConditionReferences(rule.When); err == nil {
		for _, rf := range wRefs {
			refs[rf] = true
		}
	}
	targetTools := collectRuleTargetTools(rule)
	seen := make(map[string]bool, 32)
	out := make([]AuthCell, 0, 24)
	coordKept, workerKept := 0, 0
	for cIdx, cell := range cells {
		if cell.IsBoundary {
			out = append(out, cell)
			continue
		}
		if cell.Role == "coordinator" && coordKept < 4 {
			coordKept++
			out = append(out, cell)
			continue
		}
		if cell.Role == "worker" && workerKept < 4 {
			workerKept++
			out = append(out, cell)
			continue
		}
		facts := cell.Auth.Reader.Facts()
		surfPart, profPart := "", ""
		if refs["paintedwolf.surface"] {
			surfPart = cell.SurfaceID
		}
		if refs["paintedwolf.tool_profile"] {
			profPart = cell.ProfileID
		}
		var toolBits strings.Builder
		for _, t := range targetTools {
			fmt.Fprintf(&toolBits, "|%s=%d", t, cell.Auth.Reach.Status(t))
		}
		sig := fmt.Sprintf(
			"%s|%s|%s|%s|%s|cmd=%t|req=%t|ask=%t|mp=%t|fu=%t|ws=%t|mut=%v|ext=%v|git=%v%s",
			cell.Role, cell.Mode, cell.Posture, surfPart, profPart,
			slices.Contains(cell.Auth.Offered, "command"),
			slices.Contains(cell.Auth.Offered, "request_tools"),
			slices.Contains(cell.Auth.Offered, "ask_user"),
			slices.Contains(cell.Auth.Offered, "measure_page"),
			slices.Contains(cell.Auth.Offered, "fetch_url"),
			slices.Contains(cell.Auth.Offered, "web_search"),
			facts["profile_command_mutation"],
			facts["profile_command_external_exec"],
			facts["has_git"],
			toolBits.String(),
		)
		if !seen[sig] || (rIdx+cIdx)%12 == 0 {
			seen[sig] = true
			out = append(out, cell)
		}
	}
	return out
}

// RunToolRejectedProducers discovers every compiled tool.rejected rule from
// h.Rules and exercises real BlockPlane.RejectObservation and doom-loop
// escalation producers across cells without rule-ID switches.
func (h *Harness) RunToolRejectedProducers(ctx context.Context, cells []AuthCell) ([]FiredOutcome, error) {
	var outcomes []FiredOutcome
	if len(cells) == 0 {
		return nil, nil
	}

	// Negative witness across distinct tool selectors so every tool.rejected rule records #when == false.
	negCell := cells[0]
	negTools := map[string]bool{"recall": true, "command": true, "verify": true}
	for _, r := range h.Rules.All() {
		if r != nil && r.Anchor == oar.AnchorToolRejected {
			for _, t := range r.Tools() {
				negTools[t] = true
			}
		}
	}
	sortedNeg := make([]string, 0, len(negTools))
	for t := range negTools {
		sortedNeg = append(sortedNeg, t)
	}
	slices.Sort(sortedNeg)
	for _, t := range sortedNeg {
		cctx := negCell.Context(ctx, "neg-rej-"+t)
		_ = h.BlockPlane.RejectObservation(cctx, t, "coordinator", nil, &toolrejection.ToolReject{
			Code: "UNMATCHED_NEGATIVE_WITNESS_CODE",
		})
		h.EvalCount++
	}

	// Discover compiled tool.rejected rules and generate real RejectObservation events from their compiled ASTs.
	var rejRules []*oar.Rule
	for _, r := range h.Rules.All() {
		if r != nil && r.Anchor == oar.AnchorToolRejected && r.NextSteps != nil {
			rejRules = append(rejRules, r)
		}
	}
	slices.SortFunc(rejRules, func(a, b *oar.Rule) int {
		return strings.Compare(a.Qualified(), b.Qualified())
	})

	for rIdx, rule := range rejRules {
		codes := oarcore.ConjunctValues(rule.When, "paintedwolf.rejection_code")
		if len(codes) == 0 {
			continue
		}
		nativeOps := oarcore.ConjunctValues(rule.When, "paintedwolf.native_operation")
		opConjuncts := oarcore.ConjunctValues(rule.When, "paintedwolf.operation")
		variants := h.deriveRejectOccurrenceVariants(rule)
		ruleCells := selectCellsForRejectedRule(rule, rIdx, cells)
		ruleTargetTools := collectRuleTargetTools(rule)
		for vIdx, v := range variants {
			for cIdx, cell := range ruleCells {
				offersTarget := slices.ContainsFunc(ruleTargetTools, func(t string) bool {
					return slices.Contains(cell.Auth.Offered, t)
				})
				if vIdx > 0 && cIdx > 4 && !cell.IsBoundary && !offersTarget && cIdx%4 != 0 {
					continue
				}
				invokedTool, args, data, admissible := resolveRejectedInvocation(rule, codes[0], nativeOps, opConjuncts, v, cell)
				if invokedTool == "" {
					continue
				}
				sessID := fmt.Sprintf("rej-%s-%d-%d", rule.ID, vIdx, cIdx)
				h.bindSession(sessID, cell)
				cctx := cell.Context(ctx, sessID)
				if len(nativeOps) > 0 {
					if data == nil {
						data = map[string]any{}
					}
					data["native_operation"] = nativeOps[0]
				}
				_ = h.takeAdvisories()
				h.resetExercisedEdges()
				rErr := h.BlockPlane.RejectObservation(cctx, invokedTool, cell.ProfileID, args, &toolrejection.ToolReject{
					Code: codes[0],
					Data: data,
				})
				h.EvalCount++
				evLabel := fmt.Sprintf("tool.rejected/%s[%d]", codes[0], vIdx)
				if ref, ok := guidance.RefusalFromError(rErr); ok && ref != nil {
					h.recordRefusal(&outcomes, oar.AnchorToolRejected, evLabel, cell, ref, admissible)
				}
				h.drainAdvisoriesTo(&outcomes, oar.AnchorToolRejected, evLabel, cell, admissible)
			}
		}
	}

	outcomes = append(outcomes, h.runDoomLoopRejectedProducers(ctx, cells)...)
	return outcomes, nil
}

type rejectVariant struct {
	data map[string]any
	args map[string]any
	need string
}

func (h *Harness) deriveRejectOccurrenceVariants(rule *oar.Rule) []rejectVariant {
	base := defaultOccurrenceSample(rule)
	refs := collectNextStepConditionRefs(rule)
	if refs["paintedwolf.admitted_command_forms"] || refs["paintedwolf.command_operation_reachable"] {
		dRead := maps.Clone(base)
		dRead["need"] = "read"
		dEdit := maps.Clone(base)
		dEdit["need"] = "edit"
		return []rejectVariant{{data: dRead, need: "read"}, {data: dEdit, need: "edit"}}
	}
	if refs["paintedwolf.shell_metacharacters"] || refs["paintedwolf.directory_change"] || refs["paintedwolf.env_assignment"] {
		d1 := maps.Clone(base)
		d1["shell_metacharacters"], d1["directory_change"], d1["env_assignment"] = true, true, true
		d2 := maps.Clone(base)
		d2["shell_metacharacters"], d2["reissue_via_command"], d2["directory_change"], d2["env_assignment"] = false, false, false, false
		return []rejectVariant{
			{data: d1, args: map[string]any{"command": "cd sub && FOO=1 ls | head"}},
			{data: d2, args: map[string]any{"command": "plain"}},
		}
	}
	ruleRefs, _ := oarcore.ConditionReferences(rule.When)
	for _, rf := range ruleRefs {
		refs[rf] = true
	}
	if refs["paintedwolf.tool_loadable"] || refs["paintedwolf.tool_retired_to_command"] {
		return buildToolNotOfferedVariants(base)
	}

	// Automatically generate positive and negative occurrence variants for every non-capability step.When fact!
	var variants []rejectVariant
	seen := map[string]bool{}
	addVariant := func(m map[string]any) {
		key := fmt.Sprintf("%v", m)
		if !seen[key] {
			seen[key] = true
			variants = append(variants, rejectVariant{data: m})
		}
	}
	addVariant(maps.Clone(base))

	comps := collectNextStepFactComparisons(rule)
	for fact, vals := range comps {
		short := strings.TrimPrefix(fact, "paintedwolf.")
		if h.CapabilityFacts[short] {
			continue
		}
		for _, val := range vals {
			m := maps.Clone(base)
			m[short] = val
			addVariant(m)
		}
		mZero := maps.Clone(base)
		mZero[short] = ""
		addVariant(mZero)
	}

	var occBoolAndColl []string
	for ref := range collectNextStepConditionRefs(rule) {
		short := strings.TrimPrefix(ref, "paintedwolf.")
		if !strings.HasPrefix(ref, "paintedwolf.") || h.CapabilityFacts[short] {
			continue
		}
		if _, hasComp := comps[ref]; hasComp {
			continue
		}
		occBoolAndColl = append(occBoolAndColl, ref)
	}
	slices.Sort(occBoolAndColl)
	if len(occBoolAndColl) > 0 {
		mTrue := maps.Clone(base)
		mFalse := maps.Clone(base)
		for _, ref := range occBoolAndColl {
			short := strings.TrimPrefix(ref, "paintedwolf.")
			switch h.FactTypes[ref] {
			case string(oarcore.TypeListString):
				mTrue[short] = []string{"item-1"}
				mFalse[short] = []string{}
			case string(oarcore.TypeInt):
				mTrue[short] = int64(2)
				mFalse[short] = int64(0)
			case string(oarcore.TypeString):
				mTrue[short] = "fixture-1"
				mFalse[short] = ""
			default:
				mTrue[short] = true
				mFalse[short] = false
			}
		}
		addVariant(mTrue)
		addVariant(mFalse)
	}
	if len(variants) == 0 {
		variants = append(variants, rejectVariant{data: base})
	}
	return variants
}

func buildToolNotOfferedVariants(base map[string]any) []rejectVariant {
	mk := func(kv map[string]any) rejectVariant {
		m := maps.Clone(base)
		maps.Copy(m, kv)
		return rejectVariant{data: m}
	}
	return []rejectVariant{
		mk(map[string]any{"tool_retired_to_command": true, "tool_command_forms": []string{"cat"}, "tool_loadable": false}),
		mk(map[string]any{"tool_retired_to_command": true, "tool_command_forms": []string{}, "tool_loadable": false}),
		mk(map[string]any{"tool_retired_to_command": false, "tool_retired_to_content": false, "tool_loadable": true}),
		mk(map[string]any{"tool_retired_to_command": false, "tool_retired_to_content": false, "tool_loadable": false, "tool_discovery_status": "mcp_disabled", "tool_discovery_available": false}),
		mk(map[string]any{"tool_retired_to_command": false, "tool_retired_to_content": false, "tool_loadable": false, "tool_discovery_status": "", "tool_discovery_available": true}),
	}
}

func resolveRejectedInvocation(
	rule *oar.Rule,
	code string,
	nativeOps []string,
	opConjuncts []string,
	v rejectVariant,
	cell AuthCell,
) (invokedTool string, args map[string]any, data map[string]any, admissible bool) {
	data = maps.Clone(v.data)
	args = maps.Clone(v.args)
	if v.need != "" {
		return v.need, args, data, true
	}
	if _, hasLoadable := data["tool_loadable"]; hasLoadable && code == "TOOL_NOT_OFFERED" {
		if data["tool_loadable"] == true {
			reqs := cell.Auth.Reader.Requestable()
			if len(reqs) == 0 {
				if cell.IsBoundary {
					return "ask_user", args, data, false
				}
				return "", nil, nil, false
			}
			return reqs[0], args, data, true
		}
		return "unoffered_fixture_tool", args, data, true
	}
	if len(nativeOps) > 0 {
		op := nativeOps[0]
		hasCmd := slices.Contains(cell.Auth.Offered, "command")
		admissible = hasCmd
		switch op {
		case "http_request":
			admissible = hasCmd && cell.Auth.Reader.Held()[toolcontract.ReachCommandNetworkEgress]
		case "edit", "jq_edit":
			admissible = hasCmd && cell.Auth.Reader.Held()[toolcontract.ReachCommandMutation]
		}
		if !admissible && !cell.IsBoundary {
			return "", nil, nil, false
		}
		return "command", args, data, admissible
	}
	if len(opConjuncts) > 0 {
		op := opConjuncts[0]
		if slices.Contains(cell.Auth.Offered, op) {
			return op, args, data, true
		}
		hasCmd := slices.Contains(cell.Auth.Offered, "command")
		admissible = hasCmd
		switch op {
		case "http_request":
			admissible = hasCmd && cell.Auth.Reader.Held()[toolcontract.ReachCommandNetworkEgress]
		case "write", "edit", "replace_lines", "code_rewrite", "jq_edit", "restore_version":
			admissible = hasCmd && cell.Auth.Reader.Held()[toolcontract.ReachCommandMutation]
		}
		if !admissible && !cell.IsBoundary {
			return "", nil, nil, false
		}
		return op, args, data, admissible
	}
	selTools := rule.Tools()
	if len(selTools) == 0 {
		selTools = oarcore.ConjunctValues(rule.When, "tool")
	}
	if len(selTools) > 0 {
		for _, t := range selTools {
			if slices.Contains(cell.Auth.Offered, t) {
				return t, args, data, true
			}
		}
		if cell.IsBoundary {
			return selTools[0], args, data, false
		}
		return "", nil, nil, false
	}
	if len(cell.Auth.Offered) > 0 {
		return cell.Auth.Offered[0], args, data, true
	}
	if cell.IsBoundary {
		return "command", args, data, false
	}
	return "", nil, nil, false
}

func collectNextStepConditionRefs(rule *oar.Rule) map[string]bool {
	out := map[string]bool{}
	if rule == nil || rule.NextSteps == nil {
		return out
	}
	var walk func([]oar.NextStep)
	walk = func(steps []oar.NextStep) {
		for _, st := range steps {
			if refs, err := oarcore.ConditionReferences(st.When); err == nil {
				for _, r := range refs {
					out[r] = true
				}
			}
			walk(st.Or)
		}
	}
	walk(rule.NextSteps.Steps)
	return out
}

func collectNextStepFactComparisons(rule *oar.Rule) map[string][]string {
	out := map[string][]string{}
	if rule == nil || rule.NextSteps == nil {
		return out
	}
	var walk func([]oar.NextStep)
	walk = func(steps []oar.NextStep) {
		for _, st := range steps {
			if comps, err := oarcore.ConditionFactComparisons(st.When); err == nil {
				for _, c := range comps {
					if !slices.Contains(out[c.Fact], c.Value) {
						out[c.Fact] = append(out[c.Fact], c.Value)
					}
				}
			}
			walk(st.Or)
		}
	}
	walk(rule.NextSteps.Steps)
	return out
}

func defaultOccurrenceSample(rule *oar.Rule) map[string]any {
	return map[string]any{
		"need":                    "read",
		"command":                 "npx create-app",
		"path":                    "internal/a/a.go",
		"target":                  "internal/a/a.go",
		"handle":                  "cmd-1",
		"handles_text":            "held-1, held-2",
		"limit":                   int64(4),
		"worker":                  "impl-1",
		"overlay_id":              "ov-1",
		"workflow_work_id":        "work-1",
		"state":                   "working",
		"phase":                   "execute",
		"url":                     "https://example.com",
		"query":                   "main",
		"symbol":                  "Run",
		"reason":                  "fixture",
		"rejection_reason":        "connection reset",
		"format":                  "toml",
		"kind":                    "comment",
		"line":                    "3",
		"missing_paths":           []string{"missing.go"},
		"old_string_chars":        int64(42),
		"total_lines":             int64(120),
		"change_count":            int64(2),
		"replacement_matches":     int64(0),
		"replacement_line":        int64(0),
		"http_method":             "GET",
		"http_requests_started":   int64(1),
		"http_responses_received": int64(1),
		"http_last_status":        int64(503),
		"retryable":               true,
		"code":                    rule.ID,
		"allowed":                 "[auto json yaml toml]",
	}
}
