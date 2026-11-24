package promptrender

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

type overlayEngineBundle struct {
	engine  *prompts.FileTemplateEngine
	inj     *prompts.InjectRenderer
	unitCat promptunit.Catalog
}

func runCoordinatorPromptAndProcedureMatrix(
	ctx context.Context,
	s *Stock,
	states []hoststate.CoordinatorState,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	skillsList, err := StockSkills(s)
	if err != nil {
		return err
	}
	resolver, err := newNamedWorkerResolver(ctx, s)
	if err != nil {
		return err
	}
	checkedToolDescAuth := map[string]bool{}
	bundles := map[string]overlayEngineBundle{}
	pairs, err := cachedCoordinatorPairs()
	if err != nil {
		return err
	}

	for idx, cp := range pairs {
		st, auth := cp.state, cp.auth
		if st.SessionMode == api.SessionModeAgent && st.RootCount == 0 && st.WorkflowState == hoststate.WorkflowWorkersInFlight {
			if st.TurnProfile.SurfaceID != "implement_park" || auth.Reader.Held()["pack_board"] {
				sum.Violations = append(sum.Violations, fmt.Sprintf("0-root WorkflowWorkersInFlight resolved to surface=%q pack_board=%t", st.TurnProfile.SurfaceID, auth.Reader.Held()["pack_board"]))
			} else {
				sum.FolderlessParkVerified = true
			}
		}

		bKey := engineRev(s, st.Overlay) + ":" + s.Site
		bundle, ok := bundles[bKey]
		if !ok {
			eng := engineForOverlay(s, st.Overlay)
			uc, uErr := prompts.UnitCatalogForEngine(eng)
			if uErr != nil {
				return uErr
			}
			bundle = overlayEngineBundle{
				engine:  eng,
				inj:     prompts.NewInjectRenderer(eng),
				unitCat: uc,
			}
			bundles[bKey] = bundle
		}
		resolveNamed := resolver.forCoordinatorState(st)

		if err := renderAndCheckCoordinatorStandingPrompt(ctx, s, bundle.engine, bundle.unitCat, st, auth, skillsList, idx, resolveNamed, cache, sum); err != nil {
			return err
		}
		if err := renderAndCheckToolProcedures(ctx, s, bundle.inj, "coord:"+st.ID, st.Overlay.Catalog.Revision, auth, st.SessionMode, st.Posture, resolveNamed, cache, sum); err != nil {
			return err
		}
		if !checkedToolDescAuth[auth.Digest] {
			checkedToolDescAuth[auth.Digest] = true
			checkProjectedToolDescriptions(s, st.ID, auth, st.SessionMode, st.Posture, resolveNamed, sum)
		}
	}

	// Exercise mode-transition shell and host-resources / multi-root truncation boundary renders.
	return runCoordinatorStandingBoundaryVariants(ctx, s, states, skillsList, resolver, sum)
}

func engineForOverlay(s *Stock, overlay hoststate.PackOverlay) *prompts.FileTemplateEngine {
	cat := s.Catalog
	if overlay.Catalog != nil {
		cat = overlay.Catalog
	}
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: configlayout.FindModuleRoot(),
		Site:       s.Site,
		Catalog:    cat,
	})
}

func renderAndCheckCoordinatorStandingPrompt(
	ctx context.Context,
	s *Stock,
	engine *prompts.FileTemplateEngine,
	unitCat promptunit.Catalog,
	st hoststate.CoordinatorState,
	auth hoststate.TurnAuthority,
	skillsList []skills.Skill,
	variantIdx int,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	vars, err := buildCoordinatorStandingVars(st, auth.Reader, skillsList, prompts.ExecutionModePromptTransition{}, variantIdx%2 == 0)
	if err != nil {
		return err
	}
	unitIDs := selectedUnitIDs(unitCat, auth.Reader.Selection())
	rKey := renderCacheKeyForVars("standing:"+st.TurnProfile.SurfaceID+":"+s.Site, engineRev(s, st.Overlay), unitIDs, vars)
	promptText, _, err := cache.RenderOrReuse(rKey, func() (string, error) {
		return renderStandingFromVars(ctx, engine, unitCat, st.TurnProfile, auth.Reader.Selection(), vars)
	})
	if err != nil {
		return err
	}
	sum.CoordinatorPromptCount++
	label := fmt.Sprintf("coordinator standing %s (%s)", st.TurnProfile.SurfaceID, st.ID)
	_ = cache.CheckAuthority(rKey, auth.Digest, func() error {
		viols, cErr := checkClaimsOnce(s, label, promptText, auth, st.SessionMode, st.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
		sum.Violations = append(sum.Violations, viols...)
		return cErr
	})
	return nil
}

func buildCoordinatorStandingVars(
	st hoststate.CoordinatorState,
	reader prompts.Reader,
	skillsList []skills.Skill,
	transition prompts.ExecutionModePromptTransition,
	withGlobsAndVerify bool,
) (map[string]any, error) {
	occ := cloneBaseSpawnVars()
	if len(st.Roots) > 0 {
		refs := make([]projectroot.RootRef, len(st.Roots))
		activeID := ""
		for i, r := range st.Roots {
			refs[i] = r.Ref
			if r.Ref.IsPrimary {
				activeID = r.Ref.ID
			}
		}
		prompts.MergeWorkspaceRootsVars(occ, refs, activeID)
	} else {
		prompts.MergeWorkspaceRootsVars(occ, nil, "")
	}
	verifyCmd := ""
	if withGlobsAndVerify && st.RootCount > 0 {
		verifyCmd = "./task test:digest"
		occ["read_globs"] = []string{"internal/**", "pkg/**"}
		if reader.Held()["command_mutation"] {
			occ["write_globs"] = []string{"internal/app/**", "pkg/api/**"}
		}
	}
	if err := prompts.MergeCoordinatorPromptVars(st.TurnProfile.SurfaceID, transition, verifyCmd, occ); err != nil {
		return nil, err
	}
	mergeAdmittedSkillsIntoVars(occ, reader, skillsList)
	if err := capability.MergeForTurn(occ, reader, st.SpawnAllowlist); err != nil {
		return nil, err
	}
	return prompts.Vars(reader, stripReaderFactKeys(occ))
}

var admittedSkillVarsMemo sync.Map

func mergeAdmittedSkillsIntoVars(occ map[string]any, reader prompts.Reader, skillsList []skills.Skill) {
	mKey := fmt.Sprintf("%s|%d", reader.Fingerprint(), len(skillsList))
	if cached, ok := admittedSkillVarsMemo.Load(mKey); ok {
		maps.Copy(occ, cached.(map[string]any))
		return
	}
	var views []prompts.AgentSkillView
	for _, sk := range skillsList {
		if !sk.Project && toolvocab.SkillToolsOfferedForReader(sk, reader) {
			views = append(views, prompts.AgentSkillView{
				Name:        sk.Name,
				Description: strings.TrimSpace(sk.Description),
			})
		}
	}
	surfaceVars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
		FullCatalog: true,
		Skills:      views,
	})
	admittedSkillVarsMemo.Store(mKey, surfaceVars)
	maps.Copy(occ, surfaceVars)
}

func selectedUnitIDs(cat promptunit.Catalog, sel promptunit.Selection) []string {
	rendered := cat.Rendered(sel)
	var ids []string
	for _, slot := range promptunit.Slots() {
		for _, u := range rendered[slot] {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

func renderStandingFromVars(
	ctx context.Context,
	engine *prompts.FileTemplateEngine,
	unitCat promptunit.Catalog,
	profile surface.TurnProfile,
	sel promptunit.Selection,
	baseVars map[string]any,
) (string, error) {
	vars := maps.Clone(baseVars)
	blocks, err := prompts.RenderUnitSlots(ctx, prompts.UnitRendererFor(engine), unitCat, sel, vars)
	if err != nil {
		return "", err
	}
	prompts.MergeUnitVars(vars, blocks)
	var parts []string
	for _, ref := range append([]string{surface.CoordinatorCoreTemplate}, coordinatorModeRefs(profile)...) {
		if ref == "" {
			continue
		}
		chunk, rErr := engine.Render(ctx, ref, vars)
		if rErr != nil {
			return "", rErr
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if profile.SurfaceTemplate != "" {
		chunk, rErr := engine.Render(ctx, profile.SurfaceTemplate, vars)
		if rErr != nil {
			return "", rErr
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func coordinatorModeRefs(profile surface.TurnProfile) []string {
	out := make([]string, 0, len(profile.ModeRefs))
	for _, m := range profile.ModeRefs {
		out = append(out, surface.ModeTemplateRef(m))
	}
	return out
}

func engineRev(s *Stock, overlay hoststate.PackOverlay) string {
	if overlay.Catalog != nil {
		return overlay.Catalog.Revision
	}
	return s.Catalog.Revision
}

func renderAndCheckToolProcedures(
	ctx context.Context,
	s *Stock,
	injRenderer *prompts.InjectRenderer,
	idLabel, rev string,
	auth hoststate.TurnAuthority,
	mode api.SessionMode,
	posture api.SessionPosture,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	units, err := inject.ToolProcedureUnits(injRenderer, auth.Reader)
	if err != nil {
		return err
	}
	unitIDs := make([]string, len(units))
	for i, u := range units {
		unitIDs[i] = u.ID
	}
	vars, err := prompts.Vars(auth.Reader, map[string]any{"offered_tools": auth.Reader.Offered()})
	if err != nil {
		return err
	}
	rKey := renderCacheKeyForVars("toolproc:"+s.Site, rev, unitIDs, vars)
	procText, _, err := cache.RenderOrReuse(rKey, func() (string, error) {
		return inject.RenderToolProceduresBlock(ctx, injRenderer, "sess-proc", auth.Reader)
	})
	if err != nil {
		return err
	}
	sum.CoordinatorProcCount++
	if strings.TrimSpace(procText) == "" {
		return nil
	}
	label := fmt.Sprintf("tool-procedures (%s)", idLabel)
	_ = cache.CheckAuthority(rKey, auth.Digest, func() error {
		viols, cErr := checkClaimsOnce(s, label, procText, auth, mode, posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
		sum.Violations = append(sum.Violations, viols...)
		return cErr
	})
	return nil
}

func checkProjectedToolDescriptions(
	s *Stock,
	stateID string,
	auth hoststate.TurnAuthority,
	mode api.SessionMode,
	posture api.SessionPosture,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
	sum *CartesianSummary,
) {
	blocks := toolvocabtest.HostAuthoredToolDescriptions(auth.Metas)
	evalCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:           s.Names,
		Prunable:          s.Prunable,
		Vocab:             s.Vocab,
		Self:              auth.ActorAuthority(),
		SessionMode:       mode,
		Posture:           posture,
		ResolveNamedAgent: resolveNamed,
	}
	for _, d := range blocks {
		sum.ToolDescriptionCount++
		lbl := fmt.Sprintf("projected %s.%s (%s)", d.Tool, d.FieldPath, stateID)
		cb := toolvocabtest.ClaimBlock{
			Label:   lbl,
			Subject: toolvocabtest.SubjectSelfNow(),
			Text:    d.Text,
		}
		for _, v := range toolvocabtest.EvaluateClaimBlock(evalCtx, cb) {
			sum.Violations = append(sum.Violations, fmt.Sprintf("%s names `%s`: %s", lbl, v.Claim, v.Reason))
		}
	}
}

func checkSkillRenderClaims(
	s *Stock,
	renders []SkillRender,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) {
	for _, sr := range renders {
		parts := make([]string, len(sr.Parts))
		for i, p := range sr.Parts {
			parts[i] = p.Text
		}
		joined := strings.Join(parts, "\n")
		label := fmt.Sprintf("skill %s as read by %s", sr.Skill.Name, sr.Reader)
		rKey := hoststate.RenderCacheKey("cskill:"+sr.Skill.Name, s.Catalog.Revision, nil, map[string]any{"body": joined})
		_ = cache.CheckAuthority(rKey, sr.Authority.Digest, func() error {
			viols, err := checkClaimsOnce(s, label, joined, sr.Authority, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
			sum.Violations = append(sum.Violations, viols...)
			return err
		})
	}
}

func runCoordinatorStandingBoundaryVariants(
	ctx context.Context,
	s *Stock,
	states []hoststate.CoordinatorState,
	skillsList []skills.Skill,
	resolver *namedWorkerResolver,
	sum *CartesianSummary,
) error {
	transitions := []prompts.ExecutionModePromptTransition{
		{ExecutionModePrevious: "orchestrate", ExecutionModeEntered: "investigate", ExecutionModeLeft: "orchestrate"},
		{ExecutionModePrevious: "investigate", ExecutionModeEntered: "orchestrate", ExecutionModeLeft: "investigate"},
		{ExecutionModePrevious: "orchestrate", ExecutionModeEntered: "wrapup", ExecutionModeLeft: "orchestrate"},
		{ExecutionModePrevious: "wrapup", ExecutionModeEntered: "investigate", ExecutionModeLeft: "wrapup"},
	}
	var sample hoststate.CoordinatorState
	found := false
	for _, st := range states {
		if !st.Excluded && st.PackOverlayID == "stock" && st.SessionMode == api.SessionModeAgent &&
			st.Posture == api.SessionPostureBuild && st.TurnProfile.SurfaceID == "implement_investigate" &&
			st.Loading == hoststate.LoadingAllLoaded && st.RootCount == 1 {
			sample = st
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	auth, err := hoststate.FreezeTurnAuthority(ctx, sample)
	if err != nil {
		return err
	}
	engine := s.Engine()
	unitCat, err := prompts.UnitCatalogForEngine(engine)
	if err != nil {
		return err
	}
	resolveNamed := resolver.forCoordinatorState(sample)
	for _, tr := range transitions {
		vars, vErr := buildCoordinatorStandingVars(sample, auth.Reader, skillsList, tr, true)
		if vErr != nil {
			return vErr
		}
		// Include host-resources, multi-root omitted count, and partial skill catalog variants.
		vars["agent_host_resources"] = []map[string]any{
			{
				"category": "Local tooling",
				"resources": []map[string]any{
					{"id": "go", "access": "allow", "guidance": "prefer"},
					{"id": "docker", "access": "ask", "guidance": "ask"},
					{"id": "kubectl", "access": "allow", "guidance": "avoid"},
					{"id": "aws", "access": "deny", "guidance": "deny"},
				},
			},
			{
				"category":  "",
				"resources": []map[string]any{{"id": "git", "access": "allow"}},
			},
		}
		vars["agent_host_resources_omitted"] = 2
		vars["workspace_roots_omitted_count"] = 1
		if tr.ExecutionModeEntered == "orchestrate" {
			vars["full_skill_catalog"] = false
			vars["surface_card_label"] = "orchestrate"
			vars["finish_note"] = "Summarize verified findings concisely."
		}
		text, rErr := renderStandingFromVars(ctx, engine, unitCat, sample.TurnProfile, auth.Reader.Selection(), vars)
		if rErr != nil {
			return rErr
		}
		sum.CoordinatorPromptCount++
		viols, _ := checkClaimsOnce(s, "standing-boundary/"+tr.ExecutionModeEntered, text, auth, sample.SessionMode, sample.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
		sum.Violations = append(sum.Violations, viols...)
	}
	return nil
}

func runWorkerPersonaAndInjectMatrix(
	ctx context.Context,
	s *Stock,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	engine := s.Engine()
	injRenderer := prompts.NewInjectRenderer(engine)
	skillsList, err := StockSkills(s)
	if err != nil {
		return err
	}
	checkedDesc := map[string]bool{}
	pairs, err := cachedWorkerPairs()
	if err != nil {
		return err
	}
	for idx, wp := range pairs {
		ws, auth := wp.ws, wp.auth
		mutInAsk := false
		if ws.ParentSessionMode == api.SessionModeAsk {
			if mutCapable, ok := prompts.AgentMutationCapable(ws.AgentID); ok && mutCapable {
				mutInAsk = true
			}
		}
		wEngine := engineForOverlay(s, ws.Overlay)
		wRev := s.Catalog.Revision
		if ws.Overlay.Catalog != nil {
			wRev = ws.Overlay.Catalog.Revision
		}
		if !mutInAsk {
			occ := workerPersonaOccurrence(auth.Reader, skillsList)
			if idx%2 == 0 {
				occ["agent_host_resources"] = []map[string]any{
					{"category": "Runtime", "resources": []map[string]any{{"id": "python3", "access": "allow"}}},
				}
				occ["finish_note"] = "Return evidence handles in complete_leg."
			}
			fullVars, vErr := prompts.Vars(auth.Reader, stripReaderFactKeys(occ))
			if vErr != nil {
				return vErr
			}
			rKey := renderCacheKeyForVars("wpersona:"+ws.AgentID+":"+s.Site, wRev, nil, fullVars)
			text, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
				return prompts.RenderPersona(ctx, wEngine, ws.AgentID, auth.Reader, occ)
			})
			if rErr != nil {
				return rErr
			}
			sum.WorkerArtifactCount++
			label := fmt.Sprintf("worker persona %s (%s)", ws.AgentID, ws.ID)
			_ = cache.CheckAuthority(rKey, auth.Digest, func() error {
				viols, cErr := checkClaimsOnce(s, label, text, auth, ws.ParentSessionMode, ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
				sum.Violations = append(sum.Violations, viols...)
				return cErr
			})
		}

		wInjRenderer := injRenderer
		if wEngine != engine {
			wInjRenderer = prompts.NewInjectRenderer(wEngine)
		}
		if err := renderAndCheckToolProcedures(ctx, s, wInjRenderer, "worker:"+ws.ID, wRev, auth, ws.ParentSessionMode, ws.Posture, nil, cache, sum); err != nil {
			return err
		}
		if !checkedDesc[auth.Digest] {
			checkedDesc[auth.Digest] = true
			checkProjectedToolDescriptions(s, ws.ID, auth, ws.ParentSessionMode, ws.Posture, nil, sum)
		}
	}
	return nil
}

// RenderCoordinatorStandingForTest renders the coordinator standing prompt and attached
// units for st with an optional verifyCmd, matching production standing assembly.
func RenderCoordinatorStandingForTest(
	ctx context.Context,
	s *Stock,
	st hoststate.CoordinatorState,
	verifyCmd string,
) (string, error) {
	auth, err := hoststate.FreezeTurnAuthority(ctx, st)
	if err != nil {
		return "", err
	}
	skillsList, err := StockSkills(s)
	if err != nil {
		return "", err
	}
	engine := engineForOverlay(s, st.Overlay)
	unitCat, err := prompts.UnitCatalogForEngine(engine)
	if err != nil {
		return "", err
	}
	vars, err := buildCoordinatorStandingVars(st, auth.Reader, skillsList, prompts.ExecutionModePromptTransition{}, false)
	if err != nil {
		return "", err
	}
	if verifyCmd != "" {
		occ := cloneBaseSpawnVars()
		if len(st.Roots) > 0 {
			refs := make([]projectroot.RootRef, len(st.Roots))
			activeID := ""
			for i, r := range st.Roots {
				refs[i] = r.Ref
				if r.Ref.IsPrimary {
					activeID = r.Ref.ID
				}
			}
			prompts.MergeWorkspaceRootsVars(occ, refs, activeID)
		}
		if mErr := prompts.MergeCoordinatorPromptVars(st.TurnProfile.SurfaceID, prompts.ExecutionModePromptTransition{}, verifyCmd, occ); mErr != nil {
			return "", mErr
		}
		mergeAdmittedSkillsIntoVars(occ, auth.Reader, skillsList)
		if cErr := capability.MergeForTurn(occ, auth.Reader, st.SpawnAllowlist); cErr != nil {
			return "", cErr
		}
		vars, err = prompts.Vars(auth.Reader, stripReaderFactKeys(occ))
		if err != nil {
			return "", err
		}
	}
	return renderStandingFromVars(ctx, engine, unitCat, st.TurnProfile, auth.Reader.Selection(), vars)
}

// EvaluateClaimsForTest runs checkClaimsOnce for seeded-fault and witness tests.
func EvaluateClaimsForTest(
	s *Stock,
	label, text string,
	auth hoststate.TurnAuthority,
	mode api.SessionMode,
	posture api.SessionPosture,
	subject toolvocabtest.ClaimSubject,
	checkBackticks bool,
) []string {
	viols, _ := checkClaimsOnce(s, label, text, auth, mode, posture, subject, nil, nil, nil, checkBackticks)
	return viols
}
