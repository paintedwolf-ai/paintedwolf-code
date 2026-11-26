package promptrender_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/oarproducers"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

type fuzzOverlayBundle struct {
	overlay   hoststate.PackOverlay
	stock     *promptrender.Stock
	engine    *prompts.FileTemplateEngine
	injRender *prompts.InjectRenderer
	skills    []skills.Skill
	oarLoader *oar.Loader
	oarRules  *oar.RuleSet
	rejRules  []*oar.Rule
	oarRender *oar.Renderer
}

type fuzzWorkerEnv struct {
	domains       *hoststate.SeedDomains
	overlays      []fuzzOverlayBundle
	factTypes     map[string]string
	capFacts      map[string]bool
	confirmedRuns atomic.Int64
}

var loadFuzzEnvOnce = sync.OnceValues(func() (*fuzzWorkerEnv, error) {
	ctx := context.Background()
	domains, err := hoststate.DefaultSeedDomains()
	if err != nil {
		return nil, err
	}
	baseStock, err := promptrender.LoadStock(ctx)
	if err != nil {
		return nil, err
	}
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(baseStock.Engine()))
	moduleRoot := configlayout.FindModuleRoot()
	repoRoot := filepath.Dir(moduleRoot)
	schemaDir := filepath.Join(repoRoot, "schemas")
	if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, err
	}
	if err := oar.InstallEngineDeclarationBundled(schemaDir); err != nil {
		return nil, err
	}
	declPath := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
	decl, err := oar.LoadEngineDeclaration(declPath)
	if err != nil {
		return nil, err
	}
	factTypes := make(map[string]string, len(decl.HostFacts))
	probeGC := oar.NewGuardContext()
	tools.RegisterRecoveryFacts(ctx, probeGC)
	for _, hf := range decl.HostFacts {
		factTypes[hf.Name] = hf.Type
		if err := probeGC.Ensure(hf.Name); err != nil {
			return nil, err
		}
	}
	capFacts := map[string]bool{
		"command_operation_reachable": true,
		"admitted_command_forms":      true,
		"session_mode":                true,
		"session_posture":             true,
		"session_role":                true,
		"worker_leg":                  true,
		"rejection_code":              true,
	}
	for k := range probeGC.ObservationData {
		capFacts[k] = true
	}

	bundles := make([]fuzzOverlayBundle, 0, len(domains.Overlays))
	for _, ov := range domains.Overlays {
		st := *baseStock
		st.Catalog = ov.Catalog
		eng := st.Engine()
		skList, diags := extpacks.LoadEffectiveSkills(ov.Catalog)
		if extpacks.HasErrorDiagnostic(diags) {
			return nil, fmt.Errorf("load skills for overlay %s: %+v", ov.ID, diags)
		}
		var eligibleSkills []skills.Skill
		for _, sk := range skList {
			if !sk.Project && strings.TrimSpace(sk.Metadata["authoring_reference"]) != "true" {
				eligibleSkills = append(eligibleSkills, sk)
			}
		}
		slices.SortFunc(eligibleSkills, func(a, b skills.Skill) int {
			return strings.Compare(a.Name, b.Name)
		})

		hintCfg, err := guidance.LoadHintConfigStock()
		if err != nil {
			return nil, err
		}
		loader, err := oar.NewLoader(schemaDir)
		if err != nil {
			return nil, err
		}
		loader.SetToolSchemas(baseStock.Schemas)
		loader.SetDetectors(oar.HostDetectors(nil))
		rs, err := loader.LoadEffectivePolicyWithCatalog(ov.Catalog)
		if err != nil {
			return nil, err
		}
		var rejRules []*oar.Rule
		for _, r := range rs.All() {
			if r != nil && r.Anchor == oar.AnchorToolRejected && len(oarcore.ConjunctValues(r.When, "paintedwolf.rejection_code")) > 0 {
				rejRules = append(rejRules, r)
			}
		}
		slices.SortFunc(rejRules, func(a, b *oar.Rule) int {
			return strings.Compare(a.Qualified(), b.Qualified())
		})
		bundles = append(bundles, fuzzOverlayBundle{
			overlay:   ov,
			stock:     &st,
			engine:    eng,
			injRender: prompts.NewInjectRenderer(eng),
			skills:    eligibleSkills,
			oarLoader: loader,
			oarRules:  rs,
			rejRules:  rejRules,
			oarRender: oar.NewRenderer(guidance.NewStaticRejectFormatter(hintCfg), noopNudgeFormatter{}),
		})
	}
	return &fuzzWorkerEnv{domains: domains, overlays: bundles, factTypes: factTypes, capFacts: capFacts}, nil
})

// FuzzCaseReport captures replay and audit metadata for one two-turn fuzz execution.
type FuzzCaseReport struct {
	CorpusHex            string `json:"corpus_hex"`
	DecoderVersion       uint8  `json:"decoder_version"`
	CatalogRevision      string `json:"catalog_revision"`
	Recipient            string `json:"recipient"`
	TurnACoords          string `json:"turn_a_coords"`
	TurnBCoords          string `json:"turn_b_coords"`
	SelectedContribution string `json:"selected_contribution"`
	MatchedContribution  string `json:"matched_contribution,omitempty"`
	OAROutcome           string `json:"oar_outcome,omitempty"`
	OmittedOutcome       bool   `json:"omitted_outcome,omitempty"`
	Excluded             bool   `json:"excluded,omitempty"`
	ExcludedReason       string `json:"excluded_reason,omitempty"`
	PropertyExecuted     bool   `json:"property_executed"`
	FailedProperty       string `json:"failed_property,omitempty"`
}

func mutateCoordinatorSeed(d *hoststate.SeedDomains, s hoststate.CoordinatorSeed, axisSel, valSel uint8) hoststate.CoordinatorSeed {
	out := s
	switch axisSel % 9 {
	case 0:
		out.SessionMode = d.SessionModes[(slices.Index(d.SessionModes, s.SessionMode)+1+int(valSel))%len(d.SessionModes)]
	case 1:
		out.Posture = d.Postures[int(valSel)%len(d.Postures)].ID
	case 2:
		out.SurfaceID = d.Surfaces[int(valSel)%len(d.Surfaces)].ID
	case 3:
		out.RootTopology = d.RootTopologies[int(valSel)%len(d.RootTopologies)]
	case 4:
		out.Loading = d.LoadingModes[int(valSel)%len(d.LoadingModes)]
	case 5:
		out.ApprovalPosture = d.ApprovalPostures[int(valSel)%len(d.ApprovalPostures)]
	case 6:
		out.TempState = d.TempStates[int(valSel)%len(d.TempStates)]
	case 7:
		out.MCPState = d.MCPStates[int(valSel)%len(d.MCPStates)]
	case 8:
		out.CommandExternalHidden = !s.CommandExternalHidden
	}
	return out
}

func mutateWorkerSeed(d *hoststate.SeedDomains, s hoststate.WorkerSeed, axisSel, valSel uint8) hoststate.WorkerSeed {
	out := s
	ov := d.Overlays[s.OverlayIndex]
	switch axisSel % 8 {
	case 0:
		if len(ov.WorkerAgents) > 0 {
			out.AgentID = ov.WorkerAgents[int(valSel)%len(ov.WorkerAgents)].ID
		}
	case 1:
		out.Posture = d.Postures[int(valSel)%len(d.Postures)].ID
	case 2:
		out.BranchView = d.WorkerBranches[int(valSel)%len(d.WorkerBranches)]
	case 3:
		out.Loading = d.WorkerLoadings[int(valSel)%len(d.WorkerLoadings)]
	case 4:
		out.ParentSessionMode = d.SessionModes[int(valSel)%len(d.SessionModes)]
		out.ParentUpgradedAskToAgent = out.ParentUpgradedAskToAgent && out.ParentSessionMode == api.SessionModeAsk
	case 5:
		out.CommandExternalHidden = !s.CommandExternalHidden
	case 6:
		out.ApprovalPosture = d.ApprovalPostures[int(valSel)%len(d.ApprovalPostures)]
	case 7:
		if out.RootCount == 2 {
			out.SecondaryRootGit = !s.SecondaryRootGit
		}
	}
	return out
}

// executeFuzzRecipientCase runs one deterministic two-turn recipient case.
func executeFuzzRecipientCase(ctx context.Context, env *fuzzWorkerEnv, data []byte, wsDir string) (FuzzCaseReport, error) {
	capped := data
	if len(capped) > hoststate.SeedByteLength {
		capped = capped[:hoststate.SeedByteLength]
	}
	rep := FuzzCaseReport{
		CorpusHex:      hex.EncodeToString(capped),
		DecoderVersion: hoststate.SeedCodecVersion,
	}
	sc, err := env.domains.Decode(data)
	if err != nil {
		rep.FailedProperty = "decode_seed"
		return rep, err
	}
	isWorker := sc.Worker
	numChanges := int(sc.Tail.ChangeCount) % 3
	familyIdx := int(sc.Tail.Family) % 3
	itemSel := int(sc.Tail.Item)
	changes := sc.Tail.Changes

	if !isWorker {
		rep.Recipient = "coordinator"
		seedA := sc.Coordinator
		seedB := seedA
		for _, ch := range changes[:numChanges] {
			seedB = mutateCoordinatorSeed(env.domains, seedB, ch.Axis, ch.Value)
		}
		stA, err := hoststate.ResolveCoordinatorSeed(env.domains, seedA)
		if err != nil {
			rep.FailedProperty = "resolve_turn_a"
			return rep, err
		}
		stB, err := hoststate.ResolveCoordinatorSeed(env.domains, seedB)
		if err != nil {
			rep.FailedProperty = "resolve_turn_b"
			return rep, err
		}
		rep.TurnACoords = stA.ID
		rep.TurnBCoords = stB.ID
		bundle := env.overlays[seedA.OverlayIndex]
		rep.CatalogRevision = bundle.overlay.Catalog.Revision
		if stA.Excluded || stB.Excluded {
			reason := stA.ExcludedReason
			if reason == "" {
				reason = stB.ExcludedReason
			}
			if strings.TrimSpace(reason) == "" {
				rep.FailedProperty = "excluded_without_production_reason"
				return rep, fmt.Errorf("excluded state lacked production reason: A=%s B=%s", stA.ID, stB.ID)
			}
			rep.Excluded = true
			rep.ExcludedReason = reason
			return rep, nil
		}
		return runCoordinatorTwoTurnProperties(ctx, env, bundle, stA, stB, familyIdx, itemSel, wsDir, rep)
	}

	rep.Recipient = "worker"
	if !sc.WorkerRoute {
		rep.Excluded = true
		rep.ExcludedReason = "overlay_has_no_worker_agents"
		return rep, nil
	}
	seedA := sc.WorkerSeed
	seedB := seedA
	for _, ch := range changes[:numChanges] {
		seedB = mutateWorkerSeed(env.domains, seedB, ch.Axis, ch.Value)
	}
	stA, err := hoststate.ResolveWorkerSeed(ctx, env.domains, seedA)
	if err != nil {
		rep.FailedProperty = "resolve_worker_a"
		return rep, err
	}
	stB, err := hoststate.ResolveWorkerSeed(ctx, env.domains, seedB)
	if err != nil {
		rep.FailedProperty = "resolve_worker_b"
		return rep, err
	}
	rep.TurnACoords = stA.ID
	rep.TurnBCoords = stB.ID
	bundle := env.overlays[seedA.OverlayIndex]
	rep.CatalogRevision = bundle.overlay.Catalog.Revision
	if stA.Excluded || stB.Excluded {
		reason := stA.ExcludedReason
		if reason == "" {
			reason = stB.ExcludedReason
		}
		if strings.TrimSpace(reason) == "" {
			rep.FailedProperty = "excluded_worker_without_reason"
			return rep, fmt.Errorf("excluded worker state lacked production reason: A=%s B=%s", stA.ID, stB.ID)
		}
		rep.Excluded = true
		rep.ExcludedReason = reason
		return rep, nil
	}
	return runWorkerTwoTurnProperties(ctx, env, bundle, stA, stB, familyIdx, itemSel, rep)
}

func runCoordinatorTwoTurnProperties(
	ctx context.Context,
	env *fuzzWorkerEnv,
	bundle fuzzOverlayBundle,
	stA, stB hoststate.CoordinatorState,
	familyIdx, itemSel int,
	wsDir string,
	rep FuzzCaseReport,
) (FuzzCaseReport, error) {
	authA, err := hoststate.FreezeTurnAuthority(ctx, stA)
	if err != nil {
		rep.FailedProperty = "freeze_authority_a"
		return rep, err
	}
	authB, err := hoststate.FreezeTurnAuthority(ctx, stB)
	if err != nil {
		rep.FailedProperty = "freeze_authority_b"
		return rep, err
	}

	switch familyIdx {
	case 0:
		occ := []jobOccKind{occRunning, occExitedOnly, occNoJob}[itemSel%3]
		rep.SelectedContribution = "coordinator_assembly+command_jobs:" + string(occ)
		jobs := fixedJobSnapshots(occ, jobClock)

		warmEng := newMutableCoordFuzzEngine(bundle, stA, authA, jobs)
		if _, err := assembleCoordTurn(ctx, warmEng, stA, wsDir); err != nil {
			rep.FailedProperty = "render_turn_a_warm"
			return rep, err
		}
		warmEng.retarget(stB, authB, jobs)
		warmB, err := assembleCoordTurn(ctx, warmEng, stB, wsDir)
		if err != nil {
			rep.FailedProperty = "render_turn_b_warm"
			return rep, err
		}

		freshEng := newMutableCoordFuzzEngine(bundle, stB, authB, jobs)
		freshB, err := assembleCoordTurn(ctx, freshEng, stB, wsDir)
		if err != nil {
			rep.FailedProperty = "render_turn_b_fresh"
			return rep, err
		}

		if err := compareMessagesAndProvenance(warmB, freshB); err != nil {
			rep.FailedProperty = "warm_vs_fresh_provenance_parity"
			return rep, err
		}
		evalAuthB := authB
		if len(jobs) > 0 {
			promotedB, pErr := hoststate.FreezeTurnAuthorityWithResources(ctx, stB, toolcontract.ResourcePresence{CommandJobs: true})
			if pErr != nil {
				rep.FailedProperty = "freeze_authority_b_jobs"
				return rep, pErr
			}
			evalAuthB = promotedB
		}

		hostBlocksB := newlyDeliveredHostInstructions(warmB, "")
		for _, blk := range hostBlocksB {
			if viols := promptrender.EvaluateClaimsForTest(bundle.stock, "fuzz/coord ("+stB.ID+")", blk, evalAuthB, stB.SessionMode, stB.Posture, toolvocabtest.SubjectSelfNow(), false); len(viols) > 0 {
				rep.FailedProperty = "turn_b_claim_authority"
				return rep, fmt.Errorf("claim violation on %s: %v", stB.ID, viols[0])
			}
		}
		if err := verifyJobGuidanceActions(bundle.stock.Names, hostBlocksB, jobs, evalAuthB); err != nil {
			rep.FailedProperty = "turn_b_job_occurrence_obligation"
			return rep, fmt.Errorf("state=%s instruction=command_output: %w", stB.ID, err)
		}
		if occ == occNoJob {
			rep.OmittedOutcome = true
		}

	case 1:
		return runSkillTwoTurnFuzzCheck(env, bundle, stA.ID, stB.ID, authA, authB, stA.SessionMode, stB.SessionMode, stA.Posture, stB.Posture, itemSel, rep)

	case 2:
		resA, err := promptrender.CoordinatorNamedAgentResolver(ctx, bundle.stock, stA)
		if err != nil {
			rep.FailedProperty = "resolve_named_agent_a"
			return rep, err
		}
		resB, err := promptrender.CoordinatorNamedAgentResolver(ctx, bundle.stock, stB)
		if err != nil {
			rep.FailedProperty = "resolve_named_agent_b"
			return rep, err
		}
		return runOARTwoTurnFuzzCheck(ctx, env, bundle, stA.ID, stB.ID, "coordinator", stA.TurnProfile.SurfaceID, stB.TurnProfile.SurfaceID, "coordinator", "coordinator", authA, authB, stA.SessionMode, stB.SessionMode, stA.Posture, stB.Posture, resA, resB, itemSel, rep)
	}

	env.confirmedRuns.Add(1)
	rep.PropertyExecuted = true
	return rep, nil
}

func runWorkerTwoTurnProperties(
	ctx context.Context,
	env *fuzzWorkerEnv,
	bundle fuzzOverlayBundle,
	stA, stB hoststate.WorkerState,
	familyIdx, itemSel int,
	rep FuzzCaseReport,
) (FuzzCaseReport, error) {
	authA, err := hoststate.FreezeWorkerAuthority(ctx, stA)
	if err != nil {
		rep.FailedProperty = "freeze_worker_authority_a"
		return rep, err
	}
	authB, err := hoststate.FreezeWorkerAuthority(ctx, stB)
	if err != nil {
		rep.FailedProperty = "freeze_worker_authority_b"
		return rep, err
	}

	switch familyIdx {
	case 0:
		occ := []jobOccKind{occRunning, occExitedOnly, occNoJob}[itemSel%3]
		rep.SelectedContribution = "worker_persona+command_jobs:" + stB.AgentID + ":" + string(occ)
		jobs := fixedJobSnapshots(occ, jobClock)

		warmCache := hoststate.NewRenderCache()
		_, err := renderWorkerPromptWithCache(bundle, warmCache, stA, authA)
		if err != nil {
			rep.FailedProperty = "render_worker_a_warm"
			return rep, err
		}
		warmPersonaB, err := renderWorkerPromptWithCache(bundle, warmCache, stB, authB)
		if err != nil {
			rep.FailedProperty = "render_worker_b_warm"
			return rep, err
		}
		freshCache := hoststate.NewRenderCache()
		freshPersonaB, err := renderWorkerPromptWithCache(bundle, freshCache, stB, authB)
		if err != nil {
			rep.FailedProperty = "render_worker_b_fresh"
			return rep, err
		}
		if warmPersonaB != freshPersonaB {
			rep.FailedProperty = "worker_warm_vs_fresh_parity"
			return rep, fmt.Errorf("worker persona warm vs fresh mismatch on %s", stB.ID)
		}
		if viols := promptrender.EvaluateClaimsForTest(bundle.stock, "fuzz/worker ("+stB.ID+")", warmPersonaB, authB, stB.ParentSessionMode, stB.Posture, toolvocabtest.SubjectSelfNow(), true); len(viols) > 0 {
			rep.FailedProperty = "worker_turn_b_claim_authority"
			return rep, fmt.Errorf("worker claim violation on %s: %v", stB.ID, viols[0])
		}
		jobBlock := inject.RenderCommandJobsBlock(ctx, bundle.injRender, authB.Reader, authB.Schemas["wait"], "sess-fuzz-w", jobs, nil, jobClock, "worker:"+stB.AgentID)
		var blocks []string
		if jobBlock != "" {
			blocks = []string{jobBlock}
		}
		if err := verifyJobGuidanceActions(bundle.stock.Names, blocks, jobs, authB); err != nil {
			rep.FailedProperty = "worker_job_occurrence_obligation"
			return rep, err
		}
		if occ == occNoJob {
			rep.OmittedOutcome = true
		}

	case 1:
		return runSkillTwoTurnFuzzCheck(env, bundle, stA.ID, stB.ID, authA, authB, stA.ParentSessionMode, stB.ParentSessionMode, stA.Posture, stB.Posture, itemSel, rep)

	case 2:
		workerNoDelegate := func(prof string) func(string) (toolvocabtest.ActorAuthority, error) {
			return func(agentID string) (toolvocabtest.ActorAuthority, error) {
				return toolvocabtest.ActorAuthority{}, fmt.Errorf("worker profile %s cannot delegate to %q", prof, agentID)
			}
		}
		return runOARTwoTurnFuzzCheck(ctx, env, bundle, stA.ID, stB.ID, "worker", "", "", stA.ToolProfile, stB.ToolProfile, authA, authB, stA.ParentSessionMode, stB.ParentSessionMode, stA.Posture, stB.Posture, workerNoDelegate(stA.ToolProfile), workerNoDelegate(stB.ToolProfile), itemSel, rep)
	}

	env.confirmedRuns.Add(1)
	rep.PropertyExecuted = true
	return rep, nil
}

func fixedJobSnapshots(occ jobOccKind, now time.Time) []bgprocess.JobSnapshot {
	switch occ {
	case occRunning:
		return []bgprocess.JobSnapshot{{
			Handle: "cmd-7", Mode: bgprocess.JobModeBackground, OriginTool: "command",
			StartedAt: now.Add(-5 * time.Second),
		}}
	case occExitedOnly:
		return []bgprocess.JobSnapshot{{
			Handle: "cmd-8", Mode: bgprocess.JobModeBackground, OriginTool: "command",
			StartedAt: now.Add(-12 * time.Second), Exited: true, ExitCode: 2, Refusals: 1,
		}}
	default:
		return nil
	}
}

type mutableCoordFuzzEngine struct {
	mu     sync.Mutex
	st     hoststate.CoordinatorState
	auth   hoststate.TurnAuthority
	jobs   []bgprocess.JobSnapshot
	engine *assembly.AssemblyEngine
}

type dynamicSelection struct {
	fn func() map[string]bool
}

func (d dynamicSelection) LoadedTools(string) map[string]bool {
	src := d.fn()
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (dynamicSelection) FullCatalog(string) bool { return false }

func newMutableCoordFuzzEngine(bundle fuzzOverlayBundle, st hoststate.CoordinatorState, auth hoststate.TurnAuthority, jobs []bgprocess.JobSnapshot) *mutableCoordFuzzEngine {
	m := &mutableCoordFuzzEngine{st: st, auth: auth, jobs: slices.Clone(jobs)}
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(assembly.AssemblyDeps{
		Prompts: bundle.engine,
		Injects: bundle.injRender,
		TurnReader: func(ctx context.Context, _ *api.Session, _ []api.Message, _ inject.CoordinatorTurnFrame) (prompts.Reader, error) {
			m.mu.Lock()
			st, auth, jobs := m.st, m.auth, len(m.jobs) > 0
			m.mu.Unlock()
			if !jobs {
				return auth.Reader, nil
			}
			// Live jobs promote their handle tools on the request.
			promoted, err := hoststate.FreezeTurnAuthorityWithResources(ctx, st, toolcontract.ResourcePresence{CommandJobs: true})
			return promoted.Reader, err
		},
		Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		SessionView: func(context.Context, *api.Session) *catalogview.View {
			m.mu.Lock()
			cur := m.st
			m.mu.Unlock()
			_, narrowed, err := hoststate.ExecutorForOverlay(cur.Overlay, cur.Posture, false, cur.WebSearch, cur.CommandExternalHidden, cur.MCPState)
			if err != nil {
				return &catalogview.View{Catalog: bundle.overlay.Catalog, ToolProfiles: bundle.overlay.Profiles, ToolSchemas: bundle.overlay.Schemas}
			}
			return &catalogview.View{Catalog: bundle.overlay.Catalog, ToolProfiles: narrowed, ToolSchemas: bundle.overlay.Schemas}
		},
		Selection: dynamicSelection{fn: func() map[string]bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.st.Loaded
		}},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.st.ImplementState
		},
		EffectivePromptSurface: func(context.Context, *api.Session) prompts.AgentPromptSurface {
			m.mu.Lock()
			cur := m.st
			m.mu.Unlock()
			return prompts.AgentPromptSurface{
				Posture:               cur.ApprovalPosture,
				CommandExternalHidden: cur.CommandExternalHidden,
				SessionScratchDir:     cur.SessionScratchDir,
				SharedTempLocations:   cur.SharedTempLocations,
			}
		},
		CoordinatorFrame: dynamicCoordFrame{fn: func(sess *api.Session) api.CoordinatorRunContext {
			m.mu.Lock()
			cur := m.st
			m.mu.Unlock()
			return hoststate.NormalizeCoordinatorRunContext(cur, sess, []api.Message{{Role: api.MessageRoleUser, Content: "Proceed with current inspection."}})
		}},
		WebSearchEnabled: func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.st.WebSearch
		},
		WorkspaceRoots: func(_ context.Context, sess *api.Session) ([]map[string]any, int, string, int) {
			m.mu.Lock()
			cur := m.st
			m.mu.Unlock()
			if sess == nil || cur.RootCount == 0 || strings.TrimSpace(sess.WorkspacePath) == "" {
				return nil, 0, "", 0
			}
			return ensureWorkspaceTopologyDirs(sess.WorkspacePath, cur.RootCount, cur.HasGit, cur.HasAnyGitRoot)
		},
		CommandJobs: func(string) []bgprocess.JobSnapshot {
			m.mu.Lock()
			defer m.mu.Unlock()
			return slices.Clone(m.jobs)
		},
	})
	m.engine = eng
	return m
}

type dynamicCoordFrame struct {
	fn func(*api.Session) api.CoordinatorRunContext
}

func (d dynamicCoordFrame) BuildCoordinatorTurnFrame(_ context.Context, _ string, sess *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: d.fn(sess)}, nil
}

func (m *mutableCoordFuzzEngine) retarget(st hoststate.CoordinatorState, auth hoststate.TurnAuthority, jobs []bgprocess.JobSnapshot) {
	m.mu.Lock()
	m.st = st
	m.auth = auth
	m.jobs = slices.Clone(jobs)
	m.mu.Unlock()
}

func assembleCoordTurn(ctx context.Context, m *mutableCoordFuzzEngine, st hoststate.CoordinatorState, wsDir string) ([]api.Message, error) {
	ws := wsDir
	if st.RootCount == 0 {
		ws = ""
	}
	sess := &api.Session{
		ID: "sess-fuzz-coord", ProjectID: "proj-fuzz", WorkspacePath: ws,
		AgentType: prompts.CoordinatorProfileID,
		Posture:   st.Posture, Mode: st.SessionMode,
	}
	m.engine.BeginPromptTurn(sess.ID, "")
	return m.engine.BuildCompletionMessages(ctx, sess, []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Proceed with current inspection."},
	}, nil)
}

var liveElapsedAttrRe = regexp.MustCompile(`\belapsed=[0-9a-z.<]+`)

func normalizeVolatileJobElapsed(s string) string {
	return liveElapsedAttrRe.ReplaceAllString(s, "elapsed=<live>")
}

func normalizePartsElapsed(parts []api.MessageContentPart) []api.MessageContentPart {
	if len(parts) == 0 {
		return parts
	}
	out := slices.Clone(parts)
	for i := range out {
		if out[i].Source == "command_jobs" {
			out[i].Content = normalizeVolatileJobElapsed(out[i].Content)
		}
	}
	return out
}

func compareMessagesAndProvenance(warm, fresh []api.Message) error {
	if len(warm) != len(fresh) {
		return fmt.Errorf("warm message count %d != fresh %d", len(warm), len(fresh))
	}
	for i := range warm {
		w, f := warm[i], fresh[i]
		if w.Role != f.Role || w.Origin != f.Origin || w.Authority != f.Authority || w.TrustTier != f.TrustTier {
			return fmt.Errorf("msg[%d] provenance mismatch: warm=(%s,%s,%s,%s) fresh=(%s,%s,%s,%s)",
				i, w.Role, w.Origin, w.Authority, w.TrustTier, f.Role, f.Origin, f.Authority, f.TrustTier)
		}
		wContent, fContent := w.Content, f.Content
		if strings.Contains(wContent, "## Live handles") && strings.Contains(fContent, "## Live handles") {
			wContent = normalizeVolatileJobElapsed(wContent)
			fContent = normalizeVolatileJobElapsed(fContent)
		}
		if wContent != fContent || !reflect.DeepEqual(normalizePartsElapsed(w.ContentParts), normalizePartsElapsed(f.ContentParts)) {
			return fmt.Errorf("msg[%d] content/parts warm vs fresh mismatch", i)
		}
	}
	return nil
}

func renderWorkerPromptWithCache(bundle fuzzOverlayBundle, cache *hoststate.RenderCache, st hoststate.WorkerState, auth hoststate.TurnAuthority) (string, error) {
	if st.ParentSessionMode == api.SessionModeAsk && st.MutationCapable {
		return "", nil
	}
	occ := spawn.PolicyTemplateVars()
	merged, err := prompts.Vars(auth.Reader, occ)
	if err != nil {
		return "", err
	}
	key := hoststate.RenderCacheKey("wpersona:"+st.AgentID, bundle.overlay.Catalog.Revision, nil, merged)
	text, _, err := cache.RenderOrReuse(key, func() (string, error) {
		return prompts.RenderPersona(context.Background(), bundle.engine, st.AgentID, auth.Reader, occ)
	})
	if err != nil {
		return "", err
	}
	return text, cache.CheckAuthority(key, auth.Digest, func() error { return nil })
}

func runSkillTwoTurnFuzzCheck(
	env *fuzzWorkerEnv,
	bundle fuzzOverlayBundle,
	idA, idB string,
	authA, authB hoststate.TurnAuthority,
	modeA, modeB api.SessionMode,
	postA, postB api.SessionPosture,
	itemSel int,
	rep FuzzCaseReport,
) (FuzzCaseReport, error) {
	if len(bundle.skills) == 0 {
		rep.SelectedContribution = "skill:<none>"
		env.confirmedRuns.Add(1)
		rep.PropertyExecuted = true
		return rep, nil
	}
	sk := bundle.skills[itemSel%len(bundle.skills)]
	rep.SelectedContribution = "skill:" + sk.Name

	checkTurn := func(turnID string, auth hoststate.TurnAuthority, mode api.SessionMode, post api.SessionPosture, cache *hoststate.RenderCache) (string, bool, error) {
		if !toolvocab.SkillToolsOfferedForReader(sk, auth.Reader) {
			return "", false, nil
		}
		vars, err := prompts.SkillVars(auth.Reader)
		if err != nil {
			return "", false, err
		}
		vars["configuration"] = map[string]any{}
		key := hoststate.RenderCacheKey("skill:"+sk.Name, bundle.overlay.Catalog.Revision, nil, vars)
		rendered, _, err := cache.RenderOrReuse(key, func() (string, error) {
			tpl, cErr := pongoplain.Compile(sk.Body)
			if cErr != nil {
				return "", cErr
			}
			return pongoplain.ExecuteDetached(tpl, vars)
		})
		if err != nil {
			return "", false, err
		}
		if err := cache.CheckAuthority(key, auth.Digest, func() error { return nil }); err != nil {
			return "", false, err
		}
		if viols := promptrender.EvaluateClaimsForTest(bundle.stock, "fuzz/skill/"+sk.Name+" ("+turnID+")", rendered, auth, mode, post, toolvocabtest.SubjectSelfNow(), false); len(viols) > 0 {
			return "", true, fmt.Errorf("skill %s claim violation on %s: %v", sk.Name, turnID, viols[0])
		}
		return rendered, true, nil
	}

	warmCache := hoststate.NewRenderCache()
	if _, _, err := checkTurn(idA, authA, modeA, postA, warmCache); err != nil {
		rep.FailedProperty = "skill_turn_a"
		return rep, err
	}
	warmTextB, admittedWarm, err := checkTurn(idB, authB, modeB, postB, warmCache)
	if err != nil {
		rep.FailedProperty = "skill_turn_b_warm"
		return rep, err
	}
	freshTextB, admittedFresh, err := checkTurn(idB, authB, modeB, postB, hoststate.NewRenderCache())
	if err != nil {
		rep.FailedProperty = "skill_turn_b_fresh"
		return rep, err
	}
	if admittedWarm != admittedFresh || warmTextB != freshTextB {
		rep.FailedProperty = "skill_warm_vs_fresh_parity"
		return rep, fmt.Errorf("skill %s warm vs fresh mismatch on %s", sk.Name, idB)
	}
	if !admittedWarm {
		rep.OmittedOutcome = true
	}
	env.confirmedRuns.Add(1)
	rep.PropertyExecuted = true
	return rep, nil
}

func callsSliceEqual(a, b []toolreach.Call) bool {
	return slices.EqualFunc(a, b, func(x, y toolreach.Call) bool { return reflect.DeepEqual(x, y) })
}

type oarFuzzTurnResult struct {
	outcome    string
	policyRule string
	body       string
	nextCalls  []toolreach.Call
}

func runOARTwoTurnFuzzCheck(
	ctx context.Context,
	env *fuzzWorkerEnv,
	bundle fuzzOverlayBundle,
	idA, idB, role, surfA, surfB, profA, profB string,
	authA, authB hoststate.TurnAuthority,
	modeA, modeB api.SessionMode,
	postA, postB api.SessionPosture,
	resolveNamedA, resolveNamedB func(string) (toolvocabtest.ActorAuthority, error),
	itemSel int,
	rep FuzzCaseReport,
) (FuzzCaseReport, error) {
	if len(bundle.rejRules) == 0 {
		rep.SelectedContribution = "oar:<none>"
		rep.OAROutcome = "omitted"
		rep.OmittedOutcome = true
		env.confirmedRuns.Add(1)
		rep.PropertyExecuted = true
		return rep, nil
	}
	rule := bundle.rejRules[itemSel%len(bundle.rejRules)]
	codes := oarcore.ConjunctValues(rule.When, "paintedwolf.rejection_code")
	code := codes[0]
	rep.SelectedContribution = "oar:tool.rejected:" + rule.Qualified()

	stepObs, removeStepObs := oarproducers.InstallScopedNextStepObserver(bundle.oarRules)
	defer removeStepObs()

	newIsolatedBlockPlane := func() *toolfeedback.BlockPlane {
		pipe := oar.NewGuardPipeline(bundle.oarRules, bundle.oarLoader, oar.NewCounterStore())
		pipe.SetDetectors(oar.HostDetectors(nil))
		pipe.EnableAnchor(oar.AnchorToolRejected)
		return &toolfeedback.BlockPlane{Pipeline: pipe, Renderer: bundle.oarRender}
	}

	evalTurn := func(
		bp *toolfeedback.BlockPlane,
		turnID, surf, prof string,
		auth hoststate.TurnAuthority,
		mode api.SessionMode,
		post api.SessionPosture,
		resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
	) (oarFuzzTurnResult, string, error) {
		if len(rule.Audience) > 0 && !slices.Contains(rule.Audience, prof) {
			return oarFuzzTurnResult{outcome: "omitted"}, "", nil
		}
		invoked := "command"
		selTools := rule.Tools()
		if len(selTools) == 0 {
			selTools = oarcore.ConjunctValues(rule.When, "tool")
		}
		if len(selTools) > 0 {
			invoked = ""
			for _, st := range selTools {
				if slices.Contains(auth.Offered, st) {
					invoked = st
					break
				}
			}
			if invoked == "" {
				return oarFuzzTurnResult{outcome: "omitted"}, "", nil
			}
		} else if len(auth.Offered) > 0 {
			invoked = auth.Offered[0]
		}
		parentID := ""
		if role == "worker" {
			parentID = "coord-fuzz-parent"
		}
		cctx := curationctx.WithSession(toolreach.WithRequest(ctx, auth.Reach), curationctx.Session{
			SessionID: "sess-oar-fuzz", ParentSessionID: parentID,
			Posture: string(post), Mode: string(mode), Surface: surf,
		})
		if slices.Contains(rule.RequiresFacts, "paintedwolf.mcp_error_code") {
			mcpTool := ""
			for _, tName := range auth.Offered {
				if strings.HasPrefix(tName, "mcp_") {
					mcpTool = tName
					break
				}
			}
			if mcpTool == "" {
				return oarFuzzTurnResult{outcome: "omitted"}, "", nil
			}
			invoked = mcpTool
		}
		data := map[string]any{
			"need": "read", "command": "plain", "path": "vendor/sdk.7z", "target": "a.go",
			"handle": "cmd-1", "handles_text": "held-1", "limit": int64(4), "worker": "impl-1",
			"overlay_id": "ov-1", "workflow_work_id": "work-1", "state": "working", "phase": "work",
			"url": "https://example.com", "query": "main", "symbol": "Run", "reason": "fixture",
			"rejection_reason": "reset", "format": "toml", "kind": "comment", "line": "3",
			"missing_paths": []string{"m.go"}, "old_string_chars": int64(10), "total_lines": int64(50),
			"change_count": int64(1), "replacement_matches": int64(0), "replacement_line": int64(0),
			"http_method": "GET", "http_requests_started": int64(1), "http_responses_received": int64(1),
			"http_last_status": int64(503), "retryable": true, "code": code,
			"extract_allowed": []string{".zip", ".tar", ".tar.gz"}, "extract_format_cause": "unsupported_suffix",
			"extract_invalid_format": false, "extract_rejected_value": ".7z",
			"rule_unit_id": "approvals/rules/deny-fixture", "rule_pack_id": "acme/watchdog",
			"rule_scope": "project", "rule_category": "command", "rule_pattern": "plain*",
			"error_code": "MCP_SERVER_UNAVAILABLE", "field": "path",
		}
		for _, rf := range rule.RequiresFacts {
			short := strings.TrimPrefix(rf, "paintedwolf.")
			if !strings.HasPrefix(rf, "paintedwolf.") || env.capFacts[short] {
				continue
			}
			if _, exists := data[short]; exists {
				continue
			}
			switch env.factTypes[rf] {
			case "string":
				data[short] = "fixture"
			case "integer":
				data[short] = int64(1)
			case "boolean":
				data[short] = false
			case "string_list":
				data[short] = []string{"fixture"}
			}
		}
		if ops := oarcore.ConjunctValues(rule.When, "paintedwolf.operation"); len(ops) > 0 {
			op := ops[0]
			if slices.Contains(auth.Offered, op) {
				invoked = op
			} else {
				hasCmd := slices.Contains(auth.Offered, "command")
				admissible := hasCmd
				switch op {
				case "http_request":
					admissible = hasCmd && auth.Reader.Held()[toolcontract.ReachCommandNetworkEgress]
				case "write", "edit", "replace_lines", "code_rewrite", "jq_edit", "restore_version":
					admissible = hasCmd && auth.Reader.Held()[toolcontract.ReachCommandMutation]
				}
				if !admissible {
					return oarFuzzTurnResult{outcome: "omitted"}, "", nil
				}
				invoked = op
			}
			data["operation"] = op
		}
		natOps := oarcore.ConjunctValues(rule.When, "paintedwolf.native_operation")
		if len(natOps) > 0 {
			natOp := natOps[0]
			if !slices.Contains(auth.Offered, "command") {
				return oarFuzzTurnResult{outcome: "omitted"}, "", nil
			}
			if (natOp == "edit" || natOp == "jq_edit") && !auth.Reader.Held()[toolcontract.ReachCommandMutation] {
				return oarFuzzTurnResult{outcome: "omitted"}, "", nil
			}
			if natOp == "http_request" && !auth.Reader.Held()[toolcontract.ReachCommandNetworkEgress] {
				return oarFuzzTurnResult{outcome: "omitted"}, "", nil
			}
			data["native_operation"] = natOp
			invoked = "command"
		}
		stepObs.Clear()
		argVal := slices.Contains(rule.RequiresFacts, "paintedwolf.arg_validation_errors")
		rErr := bp.RejectObservation(cctx, invoked, prof, nil, &toolrejection.ToolReject{
			Code: code, Data: data, ArgumentValidation: argVal,
		})
		if rErr == nil {
			return oarFuzzTurnResult{outcome: "omitted"}, "", nil
		}
		ref, ok := guidance.RefusalFromError(rErr)
		if !ok || ref == nil {
			return oarFuzzTurnResult{}, "oar_unexpected_error", fmt.Errorf("OAR %s returned non-refusal error on %s: %w", rule.Qualified(), turnID, rErr)
		}
		obs := oarproducers.ObserveDeliveredRefusal(ref)
		if obs.Kind == oarproducers.RefusalDeliveryFallback {
			return oarFuzzTurnResult{outcome: "fallback", body: obs.Body}, "oar_unexpected_fallback", fmt.Errorf("OAR %s degraded to fallback refusal on %s: %s", rule.Qualified(), turnID, obs.Body)
		}
		if obs.PolicyAnchor != oar.AnchorToolRejected || obs.Body == "" {
			return oarFuzzTurnResult{}, "oar_delivery_observation", fmt.Errorf("OAR %s missing anchor/body metadata on %s (anchor=%q bodyLen=%d)", rule.Qualified(), turnID, obs.PolicyAnchor, len(obs.Body))
		}
		matchedRule, resolvedSteps, hasSteps := stepObs.TakeByQualified(obs.PolicyRule)
		if matchedRule == nil {
			return oarFuzzTurnResult{}, "oar_delivery_observation", fmt.Errorf("OAR refusal reported unknown policy_rule %q on %s", obs.PolicyRule, turnID)
		}
		if hasSteps {
			if !callsSliceEqual(resolvedSteps.Calls, obs.NextCalls) {
				return oarFuzzTurnResult{}, "oar_delivery_observation", fmt.Errorf("OAR %s next_calls mismatch on %s: observed=%v delivered=%v", obs.PolicyRule, turnID, resolvedSteps.Calls, obs.NextCalls)
			}
			stepText := strings.TrimSpace(resolvedSteps.Text)
			insteadDeduped := strings.TrimSpace(ref.Copy["instead"]) == strings.TrimSpace(ref.Copy["fix"]) && !strings.Contains(ref.Copy["fix"], stepText)
			if stepText != "" && !insteadDeduped && !strings.Contains(obs.Body, stepText) {
				return oarFuzzTurnResult{}, "oar_delivery_observation", fmt.Errorf("OAR %s resolved next_steps %q absent from delivered refusal body on %s", obs.PolicyRule, stepText, turnID)
			}
		}
		if len(obs.NextCalls) > 0 && auth.Reach.Status(obs.NextCalls[0].Tool) != toolreach.Callable {
			return oarFuzzTurnResult{}, "oar_delivery_observation", fmt.Errorf("OAR %s first delivered next_call %q is not callable on %s", obs.PolicyRule, obs.NextCalls[0].Text(), turnID)
		}
		for _, c := range obs.NextCalls {
			if !strings.Contains(obs.Body, c.Text()) {
				return oarFuzzTurnResult{}, "oar_delivery_observation", fmt.Errorf("OAR %s next_call %q absent from delivered body on %s", obs.PolicyRule, c.Text(), turnID)
			}
		}
		if viols := promptrender.EvaluateDeliveredRefusalForTest(bundle.stock, "fuzz/oar/"+obs.PolicyRule+" ("+turnID+")", obs, auth, mode, post, resolveNamed); len(viols) > 0 {
			return oarFuzzTurnResult{}, "claim_authority", fmt.Errorf("OAR %s delivered-refusal claim violation on %s: %v", obs.PolicyRule, turnID, viols[0])
		}
		outcome := "matched_other_rule"
		if obs.PolicyRule == rule.Qualified() {
			outcome = "matched_selected_rule"
		}
		return oarFuzzTurnResult{
			outcome:    outcome,
			policyRule: obs.PolicyRule,
			body:       obs.Body,
			nextCalls:  obs.NextCalls,
		}, "", nil
	}

	warmBP := newIsolatedBlockPlane()
	if _, subProp, err := evalTurn(warmBP, idA, surfA, profA, authA, modeA, postA, resolveNamedA); err != nil {
		if subProp == "claim_authority" {
			rep.FailedProperty = "oar_turn_a_claim_authority"
		} else {
			rep.FailedProperty = subProp
		}
		return rep, err
	}
	warmB, subProp, err := evalTurn(warmBP, idB, surfB, profB, authB, modeB, postB, resolveNamedB)
	if err != nil {
		if subProp == "claim_authority" {
			rep.FailedProperty = "oar_turn_b_claim_authority"
		} else {
			rep.FailedProperty = subProp
		}
		return rep, err
	}
	freshB, subProp, err := evalTurn(newIsolatedBlockPlane(), idB, surfB, profB, authB, modeB, postB, resolveNamedB)
	if err != nil {
		if subProp == "claim_authority" {
			rep.FailedProperty = "oar_turn_b_fresh_claim_authority"
		} else {
			rep.FailedProperty = subProp
		}
		return rep, err
	}
	if warmB.outcome != freshB.outcome || warmB.policyRule != freshB.policyRule || warmB.body != freshB.body || !callsSliceEqual(warmB.nextCalls, freshB.nextCalls) {
		rep.FailedProperty = "oar_warm_vs_fresh_parity"
		return rep, fmt.Errorf("OAR %s warm vs fresh mismatch on %s: warm=(%s,%s) fresh=(%s,%s)\nwarmBody=%q\nfreshBody=%q\nwarmCalls=%v freshCalls=%v",
			rule.Qualified(), idB, warmB.outcome, warmB.policyRule, freshB.outcome, freshB.policyRule, warmB.body, freshB.body, warmB.nextCalls, freshB.nextCalls)
	}
	rep.OAROutcome = warmB.outcome
	if warmB.policyRule != "" {
		rep.MatchedContribution = "oar:tool.rejected:" + warmB.policyRule
	}
	if warmB.outcome != "matched_selected_rule" {
		rep.OmittedOutcome = true
	}
	env.confirmedRuns.Add(1)
	rep.PropertyExecuted = true
	return rep, nil
}
