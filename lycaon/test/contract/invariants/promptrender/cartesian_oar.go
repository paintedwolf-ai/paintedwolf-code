package promptrender

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/oarproducers"
)

type oarNextStepsBundle struct {
	loader        *oar.Loader
	rules         *oar.RuleSet
	nextStepRules []*oar.Rule
}

func loadEffectiveOARNextStepsBundle(s *Stock) (*oarNextStepsBundle, error) {
	moduleRoot := configlayout.FindModuleRoot()
	repoRoot := filepath.Dir(moduleRoot)
	if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, fmt.Errorf("install anchor catalog: %w", err)
	}
	if err := oar.InstallEngineDeclarationBundled(filepath.Join(repoRoot, "schemas")); err != nil {
		return nil, fmt.Errorf("install OAR engine declaration: %w", err)
	}
	loader, err := oar.NewLoader(filepath.Join(repoRoot, "schemas"))
	if err != nil {
		return nil, fmt.Errorf("new OAR loader: %w", err)
	}
	loader.SetToolSchemas(s.Schemas)
	loader.SetDetectors(oar.HostDetectors(nil))
	rs, err := loader.LoadEffectivePolicyWithCatalog(s.Catalog)
	if err != nil {
		return nil, fmt.Errorf("load effective OAR policy: %w", err)
	}
	var nextStepRules []*oar.Rule
	for _, r := range rs.All() {
		if r != nil && r.NextSteps != nil {
			nextStepRules = append(nextStepRules, r)
		}
	}
	slices.SortFunc(nextStepRules, func(a, b *oar.Rule) int {
		return strings.Compare(a.Qualified(), b.Qualified())
	})
	return &oarNextStepsBundle{loader: loader, rules: rs, nextStepRules: nextStepRules}, nil
}

func runOARNextStepsMatrix(
	ctx context.Context,
	s *Stock,
	_ *oarNextStepsBundle,
	coordStates []hoststate.CoordinatorState,
	workerStates []hoststate.WorkerState,
	sum *CartesianSummary,
) error {
	cells, err := buildOARAuthorityCells(ctx, s, coordStates, workerStates)
	if err != nil {
		return err
	}
	harness, err := oarproducers.NewHarness(s.Catalog, s.Schemas, s.Engine())
	if err != nil {
		return err
	}
	rejOutcomes, err := harness.RunToolRejectedProducers(ctx, cells)
	if err != nil {
		return err
	}
	hookOutcomes, err := harness.RunLifecycleHookProducers(ctx, cells)
	if err != nil {
		return err
	}
	sum.OARRuleEvalCount += harness.EvalCount

	allOutcomes := append(rejOutcomes, hookOutcomes...)
	for _, out := range allOutcomes {
		if !out.CheckClaims || strings.TrimSpace(out.ActionCopy) == "" {
			continue
		}
		label := fmt.Sprintf("oar/%s:%s (%s)", out.RuleID, out.EventLabel, out.Cell.Label)
		viols, _ := checkClaimsOnce(s, label, out.ActionCopy, out.Cell.Auth, out.Cell.Mode, out.Cell.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
		sum.Violations = append(sum.Violations, viols...)
	}
	return nil
}

// EvaluateDeliveredRefusalForTest checks a delivered OAR refusal block against
// the recipient's authority, evaluating past occurrence diagnostics under
// SubjectHistoricalObservation and host-authored guidance under SubjectSelfNow.
func EvaluateDeliveredRefusalForTest(
	s *Stock,
	label string,
	obs oarproducers.DeliveredRefusalObservation,
	auth hoststate.TurnAuthority,
	mode api.SessionMode,
	posture api.SessionPosture,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
) []string {
	var out []string
	if strings.TrimSpace(obs.Historical) != "" {
		viols, _ := checkClaimsOnce(s, label+"#occurrence", obs.Historical, auth, mode, posture, toolvocabtest.SubjectHistoricalObservation(api.MessageOriginHost), nil, nil, resolveNamed, false)
		out = append(out, viols...)
	}
	if strings.TrimSpace(obs.HostActions) != "" {
		viols, _ := checkClaimsOnce(s, label+"#actions", obs.HostActions, auth, mode, posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, false)
		out = append(out, viols...)
	}
	return out
}

func buildOARAuthorityCells(
	ctx context.Context,
	s *Stock,
	coordStates []hoststate.CoordinatorState,
	workerStates []hoststate.WorkerState,
) ([]oarproducers.AuthCell, error) {
	var out []oarproducers.AuthCell
	seen := map[string]bool{}
	addCell := func(c oarproducers.AuthCell) {
		key := fmt.Sprintf("%s|%s|%s|%s|%s", c.Auth.Digest, c.Role, c.SurfaceID, c.Mode, c.Posture)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, c)
	}

	// Discover coordinator surfaces dynamically from surfacecatalog.Load() (no hard-coded surface list).
	surfCat, err := surfacecatalog.Load()
	if err != nil {
		return nil, fmt.Errorf("load surface catalog: %w", err)
	}
	for _, surfID := range surfCat.SurfaceIDs() {
		for _, wantLoad := range []hoststate.LoadingMode{hoststate.LoadingAllLoaded, hoststate.LoadingFloorOnly} {
			wantPosture := api.SessionPostureBuild
			if surfID == "implement_investigate" && wantLoad == hoststate.LoadingFloorOnly {
				wantPosture = api.SessionPostureSpec
			}
			for _, st := range coordStates {
				if !st.Excluded && st.PackOverlayID == "stock" && st.Posture == wantPosture && st.TurnProfile.SurfaceID == surfID && st.Loading == wantLoad && st.RootCount >= 1 {
					auth, fErr := hoststate.FreezeTurnAuthority(ctx, st)
					if fErr != nil {
						return nil, fErr
					}
					addCell(oarproducers.AuthCell{
						Label:     fmt.Sprintf("coord:%s:%s:%s", surfID, wantLoad, st.Posture),
						Role:      "coordinator",
						SurfaceID: surfID,
						ProfileID: prompts.CoordinatorProfileID,
						AgentType: "coordinator",
						Mode:      st.SessionMode,
						Posture:   st.Posture,
						Auth:      auth,
						Reach:     auth.Reach,
					})
					break
				}
			}
		}
	}

	// Include posture, Ask-mode, and dynamically discovered pack-overlay coordinator states.
	for _, st := range coordStates {
		if st.Excluded || st.RootCount != 1 {
			continue
		}
		keep := (st.WebSearch && !st.CommandExternalHidden && st.TempState == hoststate.TempScratchOnly && st.ApprovalPosture == gate.PostureBalanced) ||
			st.PackOverlayID != "stock"
		if !keep {
			continue
		}
		auth, fErr := hoststate.FreezeTurnAuthority(ctx, st)
		if fErr != nil {
			return nil, fErr
		}
		addCell(oarproducers.AuthCell{
			Label:     "coord-live:" + st.ID,
			Role:      "coordinator",
			SurfaceID: st.TurnProfile.SurfaceID,
			ProfileID: prompts.CoordinatorProfileID,
			AgentType: "coordinator",
			Mode:      st.SessionMode,
			Posture:   st.Posture,
			Auth:      auth,
			Reach:     auth.Reach,
		})
		if len(out) >= 44 {
			break
		}
	}

	// Include catalog-discovered worker states across distinct worker profiles and postures.
	seenWorkerProf := map[string]int{}
	for _, ws := range workerStates {
		if ws.Excluded || ws.RootCount != 1 {
			continue
		}
		pk := ws.ToolProfile + ":" + string(ws.Posture) + ":" + string(ws.Loading)
		if seenWorkerProf[pk] >= 1 {
			continue
		}
		seenWorkerProf[pk]++
		auth, fErr := hoststate.FreezeWorkerAuthority(ctx, ws)
		if fErr != nil {
			return nil, fErr
		}
		addCell(oarproducers.AuthCell{
			Label:     "worker:" + ws.ID,
			Role:      "worker",
			ProfileID: ws.ToolProfile,
			AgentType: ws.AgentID,
			Mode:      ws.ParentSessionMode,
			Posture:   ws.Posture,
			Auth:      auth,
			Reach:     auth.Reach,
		})
	}

	out = append(out, buildOARBoundaryCells(s, out)...)
	return out, nil
}

func buildOARBoundaryCells(s *Stock, existing []oarproducers.AuthCell) []oarproducers.AuthCell {
	if len(existing) == 0 {
		return nil
	}
	base := existing[0].Auth
	for _, cand := range existing {
		if cand.Auth.ProgramTable != nil && cand.Auth.Reach.ProgramMode("external", "dispatch") && len(cand.Auth.Offered) > 2 {
			base = cand.Auth
			break
		}
	}

	var extra []oarproducers.AuthCell
	extra = append(extra, buildHeldControlsCell(s, base))
	cmdMeta, _ := s.Schemas.ToolMeta("command")
	cmdOnly := base
	cmdOnly.Metas = []tools.ToolMeta{{Name: "command", Description: cmdMeta.Description, ArgsSchema: cmdMeta.ArgsSchema}}
	cmdOnly.Offered = []string{"command"}
	cmdOnly.Plan = toolsurface.Compile([]string{"command"}, nil)
	cmdOnly.Schemas = map[string]map[string]any{"command": cmdMeta.ArgsSchema}
	cmdOnly.PostLoadSchemas = nil
	cmdOnly.Reach = toolreach.Freeze(cmdOnly.Offered, cmdOnly.Plan).WithProgramTable(base.ProgramTable).WithSchemas(cmdOnly.Schemas)
	if r, rErr := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID, Profiles: s.Profiles,
		Plan: cmdOnly.Plan, Visible: cmdOnly.Offered, Schemas: s.Schemas,
		ProjectedSchemas: cmdOnly.Schemas, RootCount: 1, HasGit: true,
	}); rErr == nil {
		cmdOnly.Reader = r
	}
	cmdOnly.Digest = "oar-cmd-only"
	for _, surf := range []string{"implement_investigate", "implement_synthesis"} {
		extra = append(extra, oarproducers.AuthCell{
			Label: "boundary:command-only:" + surf, Role: "coordinator", SurfaceID: surf,
			ProfileID: prompts.CoordinatorProfileID, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
			Auth: cmdOnly, Reach: cmdOnly.Reach, IsBoundary: true,
		})
	}

	mpMeta, _ := s.Schemas.ToolMeta("measure_page")
	mpOnly := base
	mpOnly.Metas = []tools.ToolMeta{{Name: "measure_page", Description: mpMeta.Description, ArgsSchema: mpMeta.ArgsSchema}}
	mpOnly.Offered = []string{"measure_page"}
	mpOnly.Plan = toolsurface.Compile([]string{"measure_page"}, nil)
	mpOnly.Schemas = map[string]map[string]any{"measure_page": mpMeta.ArgsSchema}
	mpOnly.PostLoadSchemas = nil
	mpOnly.Reach = toolreach.Freeze(mpOnly.Offered, mpOnly.Plan).WithSchemas(mpOnly.Schemas)
	if r, rErr := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID, Profiles: s.Profiles,
		Plan: mpOnly.Plan, Visible: mpOnly.Offered, Schemas: s.Schemas,
		ProjectedSchemas: mpOnly.Schemas, RootCount: 1, HasGit: true,
	}); rErr == nil {
		mpOnly.Reader = r
	}
	mpOnly.Digest = "oar-mp-only"
	extra = append(extra, oarproducers.AuthCell{
		Label: "boundary:measure-page-only", Role: "coordinator", SurfaceID: "implement_synthesis",
		ProfileID: prompts.CoordinatorProfileID, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
		Auth: mpOnly, Reach: mpOnly.Reach, IsBoundary: true,
	})

	reqMeta, _ := s.Schemas.ToolMeta("request_tools")
	defAskPlan := toolsurface.Compile([]string{"request_tools"}, []string{"ask_user", "command"})
	defAskSchemas := map[string]map[string]any{"request_tools": reqMeta.ArgsSchema}
	defAskLive := toolreach.Freeze([]string{"request_tools"}, defAskPlan).WithProgramTable(base.ProgramTable).WithSchemas(defAskSchemas)
	defAskAuth := base
	defAskAuth.Metas = []tools.ToolMeta{{Name: "request_tools", Description: reqMeta.Description, ArgsSchema: reqMeta.ArgsSchema}}
	defAskAuth.Offered = []string{"request_tools"}
	defAskAuth.Plan = defAskPlan
	defAskAuth.Schemas = defAskSchemas
	defAskAuth.PostLoadSchemas = nil
	defAskAuth.Reach = defAskLive
	defAskAuth.Digest = "oar-def-ask-no-schema"
	if r, rErr := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID, Profiles: s.Profiles,
		Plan: defAskPlan, Visible: []string{"request_tools"}, Schemas: s.Schemas,
		ProjectedSchemas: defAskSchemas, RootCount: 1, HasGit: true,
	}); rErr == nil {
		defAskAuth.Reader = r
	}
	extra = append(extra, oarproducers.AuthCell{
		Label: "boundary:deferred-ask-and-command-no-schema", Role: "coordinator",
		ProfileID: prompts.CoordinatorProfileID, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
		Auth: defAskAuth, Reach: defAskLive, IsBoundary: true,
	})

	emptyObj := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}
	narrowLoaderPlan := toolsurface.Compile([]string{"request_tools", "terminal_send"}, []string{"ask_user", "command", "verify"})
	narrowLoaderReach := toolreach.Freeze([]string{"request_tools", "terminal_send"}, narrowLoaderPlan).
		WithProgramTable(base.ProgramTable).
		WithSchemas(map[string]map[string]any{"request_tools": emptyObj, "terminal_send": emptyObj})
	narrowCmdReach := toolreach.Freeze([]string{"command", "terminal_send"}, toolsurface.Compile([]string{"command", "terminal_send"}, nil)).
		WithProgramTable(base.ProgramTable).
		WithSchemas(map[string]map[string]any{"command": emptyObj, "terminal_send": emptyObj})
	zero := zeroToolAuthority(base)

	for _, b := range []struct {
		lbl   string
		role  string
		surf  string
		reach toolreach.Request
	}{
		{"boundary:narrowed-loader-schema", "coordinator", "implement_synthesis", narrowLoaderReach},
		{"boundary:narrowed-command-schema", "coordinator", "implement_synthesis", narrowCmdReach},
		{"boundary:zero-tools", "coordinator", "implement_synthesis", toolreach.Offered(nil)},
		{"boundary:zero-tools-investigate", "coordinator", "implement_investigate", toolreach.Offered(nil)},
		{"boundary:zero-worker-tools", "worker", "implement_synthesis", toolreach.Offered(nil)},
	} {
		profID := prompts.CoordinatorProfileID
		if b.role == "worker" {
			profID = "implement"
		}
		extra = append(extra, oarproducers.AuthCell{
			Label: b.lbl, Role: b.role, SurfaceID: b.surf,
			ProfileID: profID, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
			Auth: zero, Reach: b.reach, IsBoundary: true,
		})
	}
	return extra
}

func buildHeldControlsCell(s *Stock, base hoststate.TurnAuthority) oarproducers.AuthCell {
	offered := []string{"command", "held_result", "held_stop"}
	schemas := make(map[string]map[string]any, len(offered))
	metas := make([]tools.ToolMeta, 0, len(offered))
	for _, name := range offered {
		meta, _ := s.Schemas.ToolMeta(name)
		schemas[name] = meta.ArgsSchema
		metas = append(metas, tools.ToolMeta{Name: name, Description: meta.Description, ArgsSchema: meta.ArgsSchema})
	}
	heldAuth := base
	heldAuth.Metas = metas
	heldAuth.Offered = offered
	heldAuth.Plan = toolsurface.Compile(offered, nil)
	heldAuth.Schemas = schemas
	heldAuth.PostLoadSchemas = nil
	heldAuth.Reach = toolreach.Freeze(offered, heldAuth.Plan).WithProgramTable(base.ProgramTable).WithSchemas(schemas)
	if r, rErr := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID, Profiles: s.Profiles,
		Plan: heldAuth.Plan, Visible: offered, Schemas: s.Schemas,
		ProjectedSchemas: schemas, RootCount: 1, HasGit: true,
	}); rErr == nil {
		heldAuth.Reader = r
	}
	heldAuth.Digest = "oar-held-controls"
	return oarproducers.AuthCell{
		Label: "boundary:held-controls", Role: "coordinator", SurfaceID: "implement_investigate",
		ProfileID: prompts.CoordinatorProfileID, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild,
		Auth: heldAuth, Reach: heldAuth.Reach, IsBoundary: true,
	}
}

// LoadOARRuleForTest loads a single effective OAR rule by ID from stock.
func LoadOARRuleForTest(s *Stock, ruleID string) (*oar.Rule, error) {
	bundle, err := loadEffectiveOARNextStepsBundle(s)
	if err != nil {
		return nil, err
	}
	r, ok := bundle.rules.Get(ruleID)
	if !ok || r == nil {
		return nil, fmt.Errorf("OAR rule %q not found", ruleID)
	}
	return r, nil
}

// EvaluateOARRuleCopyForTest evaluates ruleID through the real OAR GuardPipeline
// against st's frozen TurnAuthority.Reach and returns rendered cause + fix copy.
func EvaluateOARRuleCopyForTest(ctx context.Context, s *Stock, st hoststate.CoordinatorState, ruleID, need string) (string, error) {
	auth, err := hoststate.FreezeTurnAuthority(ctx, st)
	if err != nil {
		return "", err
	}
	return EvaluateOARRuleWithReachForTest(ctx, s, auth.Reach, st.SessionMode, st.Posture, ruleID, need)
}

// EvaluateOARRuleWithReachForTest evaluates ruleID through the real OAR GuardPipeline
// against reach and returns rendered cause + fix copy.
func EvaluateOARRuleWithReachForTest(
	ctx context.Context,
	s *Stock,
	reach toolreach.Request,
	mode api.SessionMode,
	posture api.SessionPosture,
	ruleID, need string,
) (string, error) {
	harness, err := oarproducers.NewHarness(s.Catalog, s.Schemas, s.Engine())
	if err != nil {
		return "", err
	}
	cctx := toolreach.WithRequest(ctx, reach)
	rErr := harness.BlockPlane.RejectObservation(cctx, need, prompts.CoordinatorProfileID, nil, &toolrejection.ToolReject{
		Code: ruleID,
		Data: map[string]any{"need": need},
	})
	ref, ok := guidance.RefusalFromError(rErr)
	if !ok || ref == nil {
		// Fall back to direct GuardPipeline evaluation with RegisterRecoveryFacts when ruleID is not a tool.rejected refusal.
		rule, has := harness.Rules.Get(ruleID)
		if !has || rule == nil {
			return "", fmt.Errorf("OAR rule %q not found", ruleID)
		}
		gc := oar.NewGuardContext()
		gc.Anchor = rule.Anchor
		gc.Session.SessionMode = string(mode)
		gc.Session.SessionPosture = string(posture)
		gc.Invocation.Tool = need
		gc.ObservedRejectCode = ruleID
		gc.ObservationData = map[string]any{"need": need}
		tools.RegisterRecoveryFacts(cctx, gc)
		res, eErr := harness.Pipeline.EvaluateBlock(cctx, rule.Anchor, gc)
		if eErr != nil || res == nil || res.Decision == nil {
			return "", fmt.Errorf("rule %s produced no decision: %v", ruleID, eErr)
		}
		return strings.TrimSpace(res.Decision.Copy["cause"] + " " + res.Decision.Copy["fix"]), nil
	}
	cp := ref.Copy
	return strings.TrimSpace(cp["cause"] + " " + cp["fix"]), nil
}
