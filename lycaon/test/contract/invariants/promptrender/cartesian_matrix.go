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
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// CartesianSummary aggregates reach/claim violations, render cache statistics,
// skill audit metrics, and branch/cell coverage across all 7 families.
type CartesianSummary struct {
	Violations             []string
	Coverage               hoststate.CoverageReport
	SkillAudit             SkillSurfaceAudit
	CoordinatorPromptCount int
	CoordinatorProcCount   int
	WorkerArtifactCount    int
	KickAndNudgeCount      int
	InjectAndGuidanceCount int
	ToolDescriptionCount   int
	OARRuleEvalCount       int
	CacheHits              int
	CacheMisses            int
	FolderlessParkVerified bool
}

var cartesianInvariants = []invariants.Invariant{
	{
		Name: "cartesian-7-family-matrix-names-only-offered",
		Anchors: []invariants.Anchor{
			personaRenderer,
			kickRenderer,
			invariants.Func("internal/coordinator/inject", "RenderToolProceduresBlock"),
		},
		Check: checkCartesianSevenFamilies,
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
	return invariants.Part(r.Corpus, "cartesian 7-family matrix", func() (*CartesianSummary, error) {
		s := stock(r)
		if hasSiteOverlayFiles(s.Site) {
			return &CartesianSummary{FolderlessParkVerified: true}, nil
		}
		return RunCartesianMatrix(context.Background(), s)
	})
}

func checkCartesianSevenFamilies(r *invariants.Run) {
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
}

// RunCartesianMatrix executes all 7 prompt/skill/inject/schema/OAR families across
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

	restore := tracker.Install()
	defer restore()

	cache := hoststate.NewRenderCache()
	sum := &CartesianSummary{}

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

	if err := runKicksAndInjectsMatrix(ctx, s, cache, sum); err != nil {
		return nil, err
	}
	if err := runOARNextStepsMatrix(ctx, s, oarBundle, coordStates, workerStates, sum); err != nil {
		return nil, err
	}
	if err := runCoordinatorPromptAndProcedureMatrix(ctx, s, coordStates, cache, sum); err != nil {
		return nil, err
	}
	if err := runWorkerPersonaAndInjectMatrix(ctx, s, cache, sum); err != nil {
		return nil, err
	}
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
	return sum, nil
}

func registerStockTemplatesInTracker(s *Stock, tracker *hoststate.CoverageTracker) error {
	for _, id := range s.Catalog.LoadedUnitIDs() {
		if _, _, archived := extpacks.SplitArchiveUnitID(id); archived {
			continue
		}
		rawBytes, _, ok := s.Catalog.UnitContent(id)
		if !ok {
			continue
		}
		raw := string(rawBytes)
		switch {
		case strings.HasPrefix(id, "agents/prompts/") && strings.HasSuffix(id, ".md"):
			name := strings.TrimPrefix(id, "agents/prompts/")
			if err := tracker.RegisterTemplate(id, raw, pongoplain.Composed); err != nil {
				return err
			}
			tracker.RegisterTemplateAlias("agents/"+name, id)
		case strings.HasPrefix(id, "shared/partials/") && strings.HasSuffix(id, ".md"):
			name := strings.TrimPrefix(id, "shared/partials/")
			if err := tracker.RegisterTemplate(id, raw, pongoplain.Composed); err != nil {
				return err
			}
			tracker.RegisterTemplateAlias("partials/"+name, id)
		case strings.HasPrefix(id, "shared/units/") && strings.HasSuffix(id, ".md"):
			name := strings.TrimPrefix(id, "shared/units/")
			body := promptunit.Body(rawBytes)
			if err := tracker.RegisterTemplate(id, string(body), pongoplain.Composed); err != nil {
				return err
			}
			tracker.RegisterTemplateAlias("units/"+name, id)
		case strings.HasPrefix(id, "shared/archetypes/") && strings.HasSuffix(id, ".md"):
			name := strings.TrimPrefix(id, "shared/archetypes/")
			if err := tracker.RegisterTemplate(id, raw, pongoplain.Composed); err != nil {
				return err
			}
			tracker.RegisterTemplateAlias("archetypes/"+name, id)
		case strings.HasPrefix(id, "guidance/") && strings.HasSuffix(id, ".md"):
			rel := strings.TrimPrefix(id, "guidance/")
			if err := tracker.RegisterTemplate(id, raw, pongoplain.Composed); err != nil {
				return err
			}
			tracker.RegisterTemplateAlias("guidance/"+rel, id)
			tracker.RegisterTemplateAlias("inject/"+rel, id)
			tracker.RegisterTemplateAlias("kicks/"+rel, id)
		}
	}
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
	// All 19 shipped guidance/gate-feedback/*.yaml definitions declare missing_signals with when: "true",
	// while _gate.md.tmpl retains its fallback branch as fail-closed host armor for sealed/future leaves.
	gateReason := "all 19 shipped guidance/gate-feedback/*.yaml leaves declare unconditional missing_signals (when: \"true\"), keeping matched_signals non-empty in production"
	tracker.ExpectZeroTemplateBranch("guidance/gate-feedback-templates/_gate.md.tmpl@L5:C4:if", hoststate.ExpectedZeroFalse, gateReason)
	tracker.ExpectZeroTemplateBranch("guidance/gate-feedback-templates/_gate.md.tmpl@L11:C4:else", hoststate.ExpectedZeroTrue, gateReason)

	// These 3 OAR next-step .reach checks are preceded by step.when predicates computed from the exact
	// same toolreach.Request predicate that Sequence verifies, making .reach == false structurally unreachable.
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
	memo     map[string]toolvocabtest.ActorAuthority
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
	return &namedWorkerResolver{ctx: ctx, profiles: profiles, memo: make(map[string]toolvocabtest.ActorAuthority)}, nil
}

func (nr *namedWorkerResolver) forCoordinatorState(st hoststate.CoordinatorState) func(string) (toolvocabtest.ActorAuthority, error) {
	return func(agentID string) (toolvocabtest.ActorAuthority, error) {
		profID, ok := nr.profiles[agentID]
		if !ok {
			return toolvocabtest.ActorAuthority{}, fmt.Errorf("unknown agent %q", agentID)
		}
		key := fmt.Sprintf("%s|%s|%s|%d|%t|%t|%t|%t|%s",
			st.Overlay.Catalog.Revision, st.SessionMode, st.Posture, st.RootCount,
			st.HasGit, st.HasAnyGitRoot, st.WebSearch, st.CommandExternalHidden, agentID)
		if cached, found := nr.memo[key]; found {
			return cached, nil
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
			CommandExternalHidden: st.CommandExternalHidden,
			Overlay:               st.Overlay,
		})
		if err != nil {
			return toolvocabtest.ActorAuthority{}, err
		}
		actor := wAuth.ActorAuthority()
		nr.memo[key] = actor
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
		return fmt.Sprintf("%s", v.Type())
	}
}
