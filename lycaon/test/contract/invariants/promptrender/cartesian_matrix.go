package promptrender

import (
	"context"
	"fmt"
	"maps"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// CartesianSummary aggregates reach/claim violations, render cache statistics,
// skill audit metrics, and branch/cell coverage across every recipient host state.
type CartesianSummary struct {
	Violations             []string
	Coverage               hoststate.CoverageReport
	Conditions             []hoststate.ObservedCondition
	SkillAudit             SkillSurfaceAudit
	CoordinatorPromptCount int
	CoordinatorProcCount   int
	WorkerArtifactCount    int
	KickAndNudgeCount      int
	InjectAndGuidanceCount int
	ToolDescriptionCount   int
	OARRuleEvalCount       int
	// Kicks holds each kick family's renders and violations.
	Kicks map[string]*KickFamily
	// PlainPersonas holds the worker persona renders without host resources.
	PlainPersonas KickFamily
	// setCase names the case the following renders belong to.
	setCase                func(string)
	CacheHits              int
	CacheMisses            int
	FolderlessParkVerified bool
}

// KickFamily is one render family's sweep result in the host-state matrix.
type KickFamily struct {
	Rendered   int
	Violations []string
}

const (
	kickFamilyCoordinator = "coordinator"
	kickFamilyWorker      = "worker"
	kickFamilyPhase       = "phase"
)

var cartesianInvariants = []invariants.Invariant{
	{
		Name: "host-state-matrix-names-only-offered",
		Anchors: []invariants.Anchor{
			personaRenderer,
			kickRenderer,
			invariants.Func("internal/coordinator/inject", "RenderToolProceduresBlock"),
		},
		Check: checkHostStateMatrix,
	},
	{
		Name: "stock-templates-and-oar-have-no-dead-or-vacuous-branches",
		Anchors: []invariants.Anchor{
			invariants.Decl("internal/pongoplain", "BranchRecorder"),
			invariants.Decl("internal/oar", "RuleBranchRecorder"),
		},
		Check: checkNoDeadOrVacuousBranches,
	},
}

var (
	cachedBaseSpawnVars = sync.OnceValue(spawn.PolicyTemplateVars)

	cachedStockCartesianMatrix = sync.OnceValues(func() (*CartesianSummary, error) {
		ctx := context.Background()
		s, err := LoadStock(ctx)
		if err != nil {
			return nil, err
		}
		return runCartesianMatrixUncached(ctx, s)
	})
)

func (sum *CartesianSummary) markCase(id string) {
	if sum.setCase != nil {
		sum.setCase(id)
	}
}

func cloneBaseSpawnVars() map[string]any {
	return maps.Clone(cachedBaseSpawnVars())
}

func hasSiteOverlayFiles(site string) bool {
	if strings.TrimSpace(site) == "" {
		return false
	}
	entries, err := os.ReadDir(site)
	return err == nil && len(entries) > 0
}

func cartesianSummary(r *invariants.Run) *CartesianSummary {
	return invariants.Part(r.Corpus, "host-state matrix", func() (*CartesianSummary, error) {
		return RunCartesianMatrix(context.Background(), stock(r))
	})
}

func checkHostStateMatrix(r *invariants.Run) {
	sum := cartesianSummary(r)
	r.Expect(sum.CoordinatorPromptCount, "coordinator standing prompt renders")
	r.Expect(sum.CoordinatorProcCount, "coordinator tool procedure renders")
	r.Expect(sum.WorkerArtifactCount, "worker persona and inject renders")
	r.Expect(sum.SkillAudit.AdmittedChecks, "admitted skill checks")
	r.Expect(sum.SkillAudit.ExcludedChecks, "excluded skill checks")
	r.Expect(sum.KickAndNudgeCount, "kick and nudge renders")
	r.Expect(sum.InjectAndGuidanceCount, "inject and guidance renders")
	r.Expect(sum.ToolDescriptionCount, "projected tool description checks")
	r.Expect(sum.OARRuleEvalCount, "OAR next-steps rule evaluations")
	if !sum.FolderlessParkVerified {
		r.Report("0-root WorkflowWorkersInFlight -> implement_park cell was not verified")
	}
	for _, v := range sum.Violations {
		r.Report("%s", v)
	}
}

func checkNoDeadOrVacuousBranches(r *invariants.Run) {
	sum := cartesianSummary(r)
	r.Expect(len(sum.Coverage.DiscoveredTemplates), "discovered stock templates")
	r.Expect(len(sum.Coverage.DiscoveredRules), "discovered OAR next-steps rules")
	r.Expect(len(sum.Coverage.TemplateBranches), "discovered template branches")
	r.Expect(len(sum.Coverage.RuleBranches), "discovered OAR rule branches")
	if err := sum.Coverage.Validate(); err != nil {
		r.Report("%v", err)
	}
	missing, selected, err := missingSelectedTemplates(stock(r))
	r.Must(err)
	r.Expect(selected, "templates selected by surface, mode and run-state selectors")
	for _, ref := range missing {
		r.Report("selected template %s is not shipped", ref)
	}
}

// RunCartesianMatrix executes the prompt, skill, inject, schema and OAR renders across
// hoststate.CoordinatorStates and hoststate.WorkerStates with branch tracking active.
func RunCartesianMatrix(ctx context.Context, s *Stock) (*CartesianSummary, error) {
	if !hasSiteOverlayFiles(s.Site) {
		return cachedStockCartesianMatrix()
	}
	return runCartesianMatrixUncached(ctx, s)
}

func runCartesianMatrixUncached(ctx context.Context, s *Stock) (*CartesianSummary, error) {
	tracker := hoststate.NewCoverageTracker()
	if err := registerStockTemplatesInTracker(s, tracker); err != nil {
		return nil, err
	}
	oarBundle, err := loadEffectiveOARNextStepsBundle(s)
	if err != nil {
		return nil, err
	}
	for _, rule := range oarBundle.nextStepRules {
		tracker.RegisterRule(rule)
	}
	registerExpectedZeroBranches(tracker)
	tracker.ObserveConditions(renderRecipient, occurrenceRecipient)

	restore := tracker.Install()
	defer restore()

	cache := hoststate.NewRenderCache()
	sum := &CartesianSummary{setCase: tracker.SetCase}

	coordStates, err := hoststate.CoordinatorStates(ctx)
	if err != nil {
		return nil, err
	}
	workerStates, err := hoststate.WorkerStates(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range coordStates {
		tracker.RecordCell(st.Excluded, st.ExcludedReason)
	}
	for _, ws := range workerStates {
		tracker.RecordCell(ws.Excluded, ws.ExcludedReason)
	}

	sum.markCase("kicks and injects")
	if err := runKicksAndInjectsMatrix(ctx, s, sum); err != nil {
		return nil, err
	}
	sum.markCase("OAR producers")
	if err := runOARNextStepsMatrix(ctx, s, oarBundle, coordStates, workerStates, sum); err != nil {
		return nil, err
	}
	if err := runCoordinatorPromptAndProcedureMatrix(ctx, s, coordStates, cache, sum); err != nil {
		return nil, err
	}
	if err := runWorkerPersonaAndInjectMatrix(ctx, s, cache, sum); err != nil {
		return nil, err
	}
	sum.markCase("skill audit")
	skillAudit, err := AuditStockSkillSurfaces(ctx, s, cache)
	if err != nil {
		return nil, err
	}
	sum.SkillAudit = skillAudit
	sum.Violations = append(sum.Violations, skillAudit.Violations...)
	checkSkillRenderClaims(s, skillAudit.Renders, cache, sum)

	hits, misses, _ := cache.Stats()
	sum.CacheHits = hits
	sum.CacheMisses = misses
	sum.Coverage = tracker.Report()
	sum.Conditions = tracker.Conditions()
	return sum, nil
}

func registerStockTemplatesInTracker(s *Stock, tracker *hoststate.CoverageTracker) error {
	templates, err := interactions.TemplateUnits(s.Catalog)
	if err != nil {
		return err
	}
	for id, source := range templates {
		if err := tracker.RegisterTemplate(id, source, pongoplain.Composed); err != nil {
			return err
		}
	}
	tracker.AttributeTemplateRefs(func(ref string) (string, bool) {
		return interactions.TemplateIdentity(s.Catalog, templates, ref)
	})
	gateTmplBytes, err := config.Read(config.GateFeedbackTmplDir + "/_gate.md.tmpl")
	if err != nil {
		return err
	}
	if err := tracker.RegisterTemplate("guidance/gate-feedback-templates/_gate.md.tmpl", string(gateTmplBytes), pongoplain.Isolated); err != nil {
		return err
	}
	skillsList, err := StockSkills(s)
	if err != nil {
		return err
	}
	for _, sk := range skillsList {
		if sk.Project {
			continue
		}
		if err := tracker.RegisterTemplate("skills/"+sk.Name+"/SKILL.md", sk.Body, pongoplain.Isolated); err != nil {
			return err
		}
		for _, rel := range sk.MarkdownResources() {
			resBytes, rErr := sk.ReadResource(rel)
			if rErr != nil {
				return rErr
			}
			if err := tracker.RegisterTemplate("skills/"+sk.Name+"/"+rel, string(resBytes), pongoplain.Isolated); err != nil {
				return err
			}
		}
	}
	return nil
}

func registerExpectedZeroBranches(tracker *hoststate.CoverageTracker) {
	// Every shipped gate-feedback leaf declares unconditional missing_signals,
	// so the template's fallback branch is never taken by stock leaves.
	gateReason := "every shipped guidance/gate-feedback/*.yaml leaf declares unconditional missing_signals (when: \"true\"), keeping matched_signals non-empty in production"
	tracker.ExpectZeroTemplateBranch("guidance/gate-feedback-templates/_gate.md.tmpl@L5:C4:if", hoststate.ExpectedZeroFalse, gateReason)
	tracker.ExpectZeroTemplateBranch("guidance/gate-feedback-templates/_gate.md.tmpl@L11:C4:else", hoststate.ExpectedZeroTrue, gateReason)

	// coordinator-surfaces.yaml lists command and source_history only on
	// floors, and a coordinator reader's requestable tools are its surface's
	// loadable tools (prompts.NewReader projectReach), so neither is ever
	// requestable on a coordinator turn.
	coordinatorFloorOnly := "coordinator-surfaces.yaml never lists %s as loadable; prompts.NewReader makes only the surface's loadable tools requestable"
	tracker.ExpectZeroTemplateBranch("agents/prompts/coordinator-core@L5:C473:elif", hoststate.ExpectedZeroTrue, fmt.Sprintf(coordinatorFloorOnly, "command"))
	tracker.ExpectZeroTemplateBranch("guidance/source-changes@L7:C454:elif", hoststate.ExpectedZeroTrue,
		fmt.Sprintf(coordinatorFloorOnly, "source_history")+" (inject-source-changes.yaml selects surface: coordinator)")
	// command-jobs renders only while command jobs are live.
	liveJobs := "toolcontract.Implied projects command_output and command_stop onto every turn with live command jobs, so neither is requestable while command-jobs renders"
	for _, id := range []string{"guidance/command-jobs@L8:C155:elif", "guidance/command-jobs@L8:C474:elif", "guidance/command-jobs@L9:C250:elif"} {
		tracker.ExpectZeroTemplateBranch(id, hoststate.ExpectedZeroTrue, liveJobs)
	}
	tracker.ExpectZeroTemplateBranch("shared/partials/agent-tool-surface@L33:C4:if", hoststate.ExpectedZeroFalse,
		"prompts.fillWorkerReaderFacts sets reject_codes from recoverableRejectCodes over the host's shipped hint catalog, which is never empty")
	runnerReach := "native-tools.yaml: command and verify each accept capability_request.write_root, local_listen, loopback_connect and socket_paths, " +
		"and no runner tool reaches a turn without workspace roots (no_folder_allowlist omits them; internal/worker/task_tool.go WORKER_WORKSPACE_REQUIRED)"
	for _, id := range []string{"shared/partials/capability-request-grammar@L1:C320:if", "shared/partials/capability-request-grammar@L1:C492:if", "shared/partials/capability-request-grammar@L1:C732:if"} {
		tracker.ExpectZeroTemplateBranch(id, hoststate.ExpectedZeroFalse, runnerReach)
	}
	effectFlags := "prompts/reader_facts.go sets the profile_command_* effect flags only when command is offered, and native-edit-tool renders these lines " +
		"and includes large-file-write-discipline only under profile_command_mutation"
	for _, id := range []string{"shared/units/native-edit-tool@L5:C261:if", "shared/partials/large-file-write-discipline@L1:C64:if",
		"shared/partials/large-file-write-discipline@L2:C4:if", "shared/partials/large-file-write-discipline@L2:C207:if"} {
		tracker.ExpectZeroTemplateBranch(id, hoststate.ExpectedZeroFalse, effectFlags)
	}
	commandRoots := "command reaches no turn without workspace roots: coordinator-surfaces.yaml no_folder_allowlist omits it, and internal/worker/task_tool.go refuses a rootless worker whose profile surveys with command (WORKER_WORKSPACE_REQUIRED)"
	tracker.ExpectZeroTemplateBranch("shared/partials/command-surface@L5:C4:if", hoststate.ExpectedZeroFalse, commandRoots)
	tracker.ExpectZeroTemplateBranch("skills/diagnose-performance/references/diagnose_resource_exhaustion.md@L17:C440:if", hoststate.ExpectedZeroFalse, commandRoots)

	// Each step.when below is computed from the same toolreach.Request
	// predicate Sequence verifies, so .reach == false cannot occur.
	tracker.ExpectZeroRuleBranch("PROCESS_UNSUPPORTED", "steps[0].reach", hoststate.ExpectedZeroFalse,
		"step.when size(paintedwolf.admitted_process_capability_fields) > 0 requires request.AcceptsField(\"command\", ...), which structurally implies Sequence(command) == true")
	tracker.ExpectZeroRuleBranch("TOOL_ACTION_IN_TEXT", "steps[0].reach", hoststate.ExpectedZeroFalse,
		"step.when size(paintedwolf.text_action_command_forms) > 0 requires gc.Reach.Status(\"command\") == Callable, which structurally implies Sequence(command) == true")
	tracker.ExpectZeroRuleBranch("TOOL_COMMAND_OPERATION", "steps[0].reach", hoststate.ExpectedZeroFalse,
		"step.when paintedwolf.command_operation_reachable requires ObserveCommandOperationReach() to verify command is Callable/Loadable with >=1 admitted program form, which structurally implies Sequence(command) == true")
}

type namedWorkerResolver struct {
	ctx      context.Context
	profiles map[string]string
	memo     sync.Map
}

func newNamedWorkerResolver(ctx context.Context, s *Stock) (*namedWorkerResolver, error) {
	defs, err := agentdef.LoadEffectiveWithCatalog(s.Catalog)
	if err != nil {
		return nil, err
	}
	profiles := make(map[string]string, len(defs))
	for _, d := range defs {
		profiles[d.ID] = d.ToolProfile
	}
	return &namedWorkerResolver{ctx: ctx, profiles: profiles}, nil
}

// CoordinatorNamedAgentResolver returns a thread-safe named-agent authority resolver
// parameterized by st's actual session mode, posture, workspace topology, and settings.
func CoordinatorNamedAgentResolver(ctx context.Context, s *Stock, st hoststate.CoordinatorState) (func(string) (toolvocabtest.ActorAuthority, error), error) {
	nr, err := newNamedWorkerResolver(ctx, s)
	if err != nil {
		return nil, err
	}
	return nr.forCoordinatorState(st), nil
}

func (nr *namedWorkerResolver) forCoordinatorState(st hoststate.CoordinatorState) func(string) (toolvocabtest.ActorAuthority, error) {
	return func(agentID string) (toolvocabtest.ActorAuthority, error) {
		profID, ok := nr.profiles[agentID]
		if !ok {
			return toolvocabtest.ActorAuthority{}, fmt.Errorf("unknown agent %q", agentID)
		}
		rev := ""
		if st.Overlay.Catalog != nil {
			rev = st.Overlay.Catalog.Revision
		}
		key := fmt.Sprintf("%s|%s|%s|%d|%t|%t|%t|%t|%t|%s|%s",
			rev, st.SessionMode, st.Posture, st.RootCount,
			st.HasGit, st.HasAnyGitRoot, st.WebSearch, st.Vision, st.CommandExternalHidden, st.ApprovalPosture, agentID)
		if cached, found := nr.memo.Load(key); found {
			return cached.(toolvocabtest.ActorAuthority), nil
		}
		wAuth, err := hoststate.FreezeWorkerAuthority(nr.ctx, hoststate.WorkerState{
			AgentID:               agentID,
			ToolProfile:           profID,
			ParentSessionMode:     st.SessionMode,
			Posture:               st.Posture,
			RootCount:             st.RootCount,
			HasGit:                st.HasGit,
			HasAnyGitRoot:         st.HasAnyGitRoot,
			WebSearch:             st.WebSearch,
			Vision:                st.Vision,
			CommandExternalHidden: st.CommandExternalHidden,
			ApprovalPosture:       st.ApprovalPosture,
			Overlay:               st.Overlay,
		})
		if err != nil {
			return toolvocabtest.ActorAuthority{}, err
		}
		actor := wAuth.ActorAuthority()
		nr.memo.Store(key, actor)
		return actor, nil
	}
}

func renderCacheKeyForVars(templateID, catalogRevision string, selectedUnits []string, fullVars map[string]any) string {
	if key := hoststate.RenderCacheKey(templateID, catalogRevision, selectedUnits, fullVars); key != "" {
		return key
	}
	projected, ok := projectCacheableValue(reflect.ValueOf(fullVars)).(map[string]any)
	if !ok {
		return ""
	}
	return hoststate.RenderCacheKey(templateID, catalogRevision, selectedUnits, projected)
}

func projectCacheableValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = projectCacheableValue(v.Index(i))
		}
		return out
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Sprintf("%v", v.Interface())
		}
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = projectCacheableValue(iter.Value())
		}
		return out
	case reflect.Struct:
		t := v.Type()
		out := make(map[string]any, v.NumField()+1)
		out["__type__"] = t.String()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if f.IsExported() {
				out[f.Name] = projectCacheableValue(v.Field(i))
			}
		}
		return out
	default:
		return v.Type().String()
	}
}
