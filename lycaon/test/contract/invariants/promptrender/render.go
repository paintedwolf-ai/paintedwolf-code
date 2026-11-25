// Package promptrender renders the stock prompts across the Cartesian hoststate
// matrix and holds the invariants over what renders: every persona, template,
// unit, procedure, kick, inject, and skill renders, fits its budget, keeps its
// prose intact, covers both branch outcomes, and names only what its reader can reach.
package promptrender

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// Stock is the stock catalog with the tool profiles and tool vocabulary a
// render reads.
type Stock struct {
	Catalog  *extpacks.EffectiveCatalog
	Profiles []sandbox.ToolProfile
	Schemas  *toolschema.Config
	Names    *toolvocab.Catalog
	Prunable *toolvocabtest.PrunableSchemaIndex
	Vocab    *toolvocabtest.BacktickVocab
	// Site is the checkout's distribution prompt overlay, which a render
	// reads before the bundled templates.
	Site string
}

// Engine renders the stock templates under the checkout's site overlay.
func (s *Stock) Engine() *prompts.FileTemplateEngine {
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: configlayout.FindModuleRoot(),
		Site:       s.Site,
		Catalog:    s.Catalog,
	})
}

// LoadStock resolves the stock catalog with no device choices.
func LoadStock(ctx context.Context) (*Stock, error) {
	if anchor.DefaultRegistry() == nil {
		if reg, regErr := anchor.LoadRegistryFromConfigRoot(); regErr == nil {
			anchor.SetDefaultRegistry(reg)
		}
	}
	content, err := extpacks.DiscoverStockContent()
	if err != nil {
		return nil, err
	}
	eff := extpacks.Resolve(ctx, extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("load tool profiles: %w", err)
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, fmt.Errorf("load tool schemas: %w", err)
	}
	names, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	if err != nil {
		return nil, err
	}
	prunable, err := toolvocabtest.StockPrunableSchemaIndex()
	if err != nil {
		return nil, fmt.Errorf("load prunable schema index: %w", err)
	}
	vocab, err := toolvocabtest.StockBacktickVocab()
	if err != nil {
		return nil, fmt.Errorf("load backtick vocab: %w", err)
	}
	return &Stock{
		Catalog:  eff,
		Profiles: profiles,
		Schemas:  schemas,
		Names:    names,
		Prunable: prunable,
		Vocab:    vocab,
	}, nil
}

// PersonaEngine renders personas and agent templates from the module.
func PersonaEngine() *prompts.FileTemplateEngine {
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
}

// Text is one rendered text.
type Text struct {
	Label    string
	Text     string
	Resource bool
}

// SkillRender is one stock skill rendered for one reader authority.
type SkillRender struct {
	Skill     skills.Skill
	Reader    string
	State     prompts.Reader
	Authority hoststate.TurnAuthority
	Parts     []Text
}

// SkillSurfaceAudit summarizes skill discovery, preload, exclusion, and schema-narrowing checks.
type SkillSurfaceAudit struct {
	Renders                 []SkillRender
	AdmittedChecks          int
	ExcludedChecks          int
	DiscoveryChecks         int
	PreloadProcedureChecks  int
	PreloadPointerChecks    int
	WarmColdNarrowingChecks int
	Violations              []string
}

// RenderStockSkills renders every stock skill across hoststate coordinator and
// worker states (floor_only, partly_loaded, all_loaded, all 4 postures,
// Agent/Ask, WebSearch on/off, mixed 2-root Git orders, external hidden, temp
// states, and pack overlays), deduplicating identical var-map renders while
// retaining every distinct authority projection.
func RenderStockSkills(stock *Stock) ([]SkillRender, error) {
	audit, err := AuditStockSkillSurfaces(context.Background(), stock, hoststate.NewRenderCache())
	if err != nil {
		return nil, err
	}
	if len(audit.Violations) > 0 {
		return nil, fmt.Errorf("skill surface audit violations:\n%s", strings.Join(audit.Violations, "\n"))
	}
	return audit.Renders, nil
}

var cachedStockSkillAudit = sync.OnceValues(func() (SkillSurfaceAudit, error) {
	ctx := context.Background()
	s, err := LoadStock(ctx)
	if err != nil {
		return SkillSurfaceAudit{}, err
	}
	return auditStockSkillSurfacesUncached(ctx, s, hoststate.NewRenderCache())
})

// AuditStockSkillSurfaces evaluates skill admission, exclusion, discovery
// listings, preloads, warm/cold projected-schema narrowing, and body/resource
// renders across CoordinatorStates and WorkerStates.
func AuditStockSkillSurfaces(ctx context.Context, stock *Stock, cache *hoststate.RenderCache) (SkillSurfaceAudit, error) {
	if stock != nil && !hasSiteOverlayFiles(stock.Site) && pongoplain.ActiveBranchRecorder.Load() == nil {
		if defaultStock, err := LoadStock(ctx); err == nil && defaultStock.Catalog.Revision == stock.Catalog.Revision {
			return cachedStockSkillAudit()
		}
	}
	return auditStockSkillSurfacesUncached(ctx, stock, cache)
}

func auditStockSkillSurfacesUncached(ctx context.Context, stock *Stock, cache *hoststate.RenderCache) (SkillSurfaceAudit, error) {
	var audit SkillSurfaceAudit
	loaded, err := StockSkills(stock)
	if err != nil {
		return audit, err
	}
	if cache == nil {
		cache = hoststate.NewRenderCache()
	}

	injRenderer := prompts.NewInjectRenderer(stock.Engine())
	seenRenderAuth := map[string]bool{}
	seenReaderFP := map[string]bool{}
	seenPreloadFP := map[string]bool{}
	unconditionalSkills := map[string]bool{}
	emptyReader, err := toolvocabtest.ReaderHolding(nil)
	if err != nil {
		return SkillSurfaceAudit{}, err
	}
	for _, sk := range loaded {
		if !sk.Project && toolvocab.SkillToolsOfferedForReader(sk, emptyReader) {
			unconditionalSkills[sk.Name] = true
		}
	}

	processState := func(label, profileID string, sessionMode api.SessionMode, posture api.SessionPosture, auth hoststate.TurnAuthority, effectiveSkills []skills.Skill) error {
		fp := auth.Digest + "|" + auth.Reader.Fingerprint() + "|" + string(sessionMode) + "|" + string(posture) + "|" + profileID
		if seenReaderFP[fp] {
			return nil
		}
		seenReaderFP[fp] = true

		hasProcExec := profileHasProcessExecSurface(stock.Profiles, profileID, sessionMode, posture)
		vars, vErr := prompts.SkillVars(auth.Reader)
		if vErr != nil {
			return vErr
		}
		varsWithCfg := make(map[string]any, len(vars)+1)
		for k, v := range vars {
			varsWithCfg[k] = v
		}
		varsWithCfg["configuration"] = map[string]any{}
		varsDigest := renderCacheKeyForVars("skillvars", stock.Catalog.Revision, nil, varsWithCfg)

		var admitted []skills.Skill
		renderedBody := make(map[string]string, len(loaded))
		for _, sk := range loaded {
			if sk.Project {
				continue
			}
			// If an overlay removed the skill's pack, only test skills in effectiveSkills.
			if len(effectiveSkills) > 0 && !slices.ContainsFunc(effectiveSkills, func(e skills.Skill) bool { return e.Name == sk.Name }) {
				continue
			}
			if strings.TrimSpace(sk.Metadata["host_resources"]) != "" && !hasProcExec {
				audit.ExcludedChecks++
				continue
			}
			if !toolvocab.SkillToolsOfferedForReader(sk, auth.Reader) {
				audit.ExcludedChecks++
				if unconditionalSkills[sk.Name] {
					audit.Violations = append(audit.Violations, fmt.Sprintf("%s excluded unconditional skill %s", label, sk.Name))
				}
				continue
			}
			audit.AdmittedChecks++
			admitted = append(admitted, sk)

			parts, rErr := renderSkillCachedWithDigest(ctx, cache, varsDigest, sk, varsWithCfg)
			if rErr != nil {
				return rErr
			}
			if len(parts) > 0 {
				renderedBody[sk.Name] = parts[0].Text
			}
			pairKey := sk.Name + "\x00" + auth.Digest + "\x00" + fmt.Sprintf("%v", vars["can_git_read"]) + fmt.Sprintf("%v", vars["can_git_commit"]) + fmt.Sprintf("%v", vars["can_ask_user_secret"]) + fmt.Sprintf("%v", vars["session_scratch_dir"]) + fmt.Sprintf("%v", vars["shared_temp_writable"]) + fmt.Sprintf("%v", vars["on_worker_branch"])
			if !seenRenderAuth[pairKey] {
				seenRenderAuth[pairKey] = true
				audit.Renders = append(audit.Renders, SkillRender{
					Skill:     sk,
					Reader:    label,
					State:     auth.Reader,
					Authority: auth,
					Parts:     parts,
				})
			}
		}

		preloadKey := fmt.Sprintf("%s|%s|%t", profileID, auth.Reader.Fingerprint(), slices.Contains(auth.Offered, "skills_read"))
		var preloadInj *prompts.InjectRenderer
		if !seenPreloadFP[preloadKey] {
			seenPreloadFP[preloadKey] = true
			preloadInj = injRenderer
		}
		verifySkillDiscoveryAndPreloads(ctx, preloadInj, label, profileID, auth, admitted, renderedBody, &audit)
		return nil
	}

	coordPairs, err := cachedCoordinatorPairs()
	if err != nil {
		return audit, err
	}
	overlaySkillsCache := map[string][]skills.Skill{}
	for _, cp := range coordPairs {
		st := cp.state
		var overlaySkills []skills.Skill
		if st.Overlay.Catalog != nil && st.PackOverlayID != "stock" {
			cached, found := overlaySkillsCache[st.PackOverlayID]
			if !found {
				var diags []extpacks.Diagnostic
				cached, diags = extpacks.LoadEffectiveSkills(st.Overlay.Catalog)
				if extpacks.HasErrorDiagnostic(diags) {
					return audit, fmt.Errorf("load skills for overlay %s: %+v", st.PackOverlayID, diags)
				}
				overlaySkillsCache[st.PackOverlayID] = cached
			}
			overlaySkills = cached
		}
		if err := processState(st.ID, prompts.CoordinatorProfileID, st.SessionMode, st.Posture, cp.auth, overlaySkills); err != nil {
			return audit, err
		}
	}

	workerPairs, err := cachedWorkerPairs()
	if err != nil {
		return audit, err
	}
	for _, wp := range workerPairs {
		if err := processState(wp.ws.ID, wp.ws.ToolProfile, wp.ws.ParentSessionMode, wp.ws.Posture, wp.auth, nil); err != nil {
			return audit, err
		}
	}

	boundaryAuths, err := skillBoundaryAuthorities(stock, coordPairs, loaded)
	if err != nil {
		return audit, err
	}
	for _, sb := range boundaryAuths {
		if err := processState(sb.label, sb.profileID, sb.mode, sb.posture, sb.auth, sb.onlySkills); err != nil {
			return audit, err
		}
	}

	if err := verifyWarmColdSkillSchemaNarrowing(ctx, stock, loaded, &audit); err != nil {
		return audit, err
	}
	return audit, nil
}

var compiledSkillTplCache sync.Map

func compileSkillTemplateCached(unitID, text string) (*pongo2.Template, error) {
	cKey := unitID + "\x00" + pongoplain.InlineTemplateID(text)
	if v, ok := compiledSkillTplCache.Load(cKey); ok {
		return v.(*pongo2.Template), nil
	}
	tpl, err := pongoplain.Compile(text)
	if err != nil {
		return nil, err
	}
	compiledSkillTplCache.Store(cKey, tpl)
	return tpl, nil
}

func renderSkillCached(
	ctx context.Context,
	cache *hoststate.RenderCache,
	catalogRev string,
	sk skills.Skill,
	vars map[string]any,
) ([]Text, error) {
	data := make(map[string]any, len(vars)+1)
	for name, value := range vars {
		data[name] = value
	}
	data["configuration"] = map[string]any{}
	varsDigest := renderCacheKeyForVars("skillvars", catalogRev, nil, data)
	return renderSkillCachedWithDigest(ctx, cache, varsDigest, sk, data)
}

func renderSkillCachedWithDigest(
	ctx context.Context,
	cache *hoststate.RenderCache,
	varsDigest string,
	sk skills.Skill,
	data map[string]any,
) ([]Text, error) {
	renderOne := func(text, unitID string, resource bool) (Text, error) {
		var key string
		if varsDigest != "" {
			key = "skill:" + unitID + ":" + varsDigest
		}
		out, _, err := cache.RenderOrReuse(key, func() (string, error) {
			tpl, cErr := compileSkillTemplateCached(unitID, text)
			if cErr != nil {
				return "", fmt.Errorf("compile %s: %w", unitID, cErr)
			}
			rOut, eErr := pongoplain.Execute(ctx, tpl, data)
			if eErr != nil {
				return "", fmt.Errorf("render %s: %w", unitID, eErr)
			}
			return rOut, nil
		})
		if err != nil {
			return Text{}, err
		}
		return Text{Label: unitID, Text: out, Resource: resource}, nil
	}

	body, err := renderOne(sk.Body, sk.Name, false)
	if err != nil {
		return nil, err
	}
	parts := []Text{body}
	for _, rel := range sk.MarkdownResources() {
		raw, rErr := sk.ReadResource(rel)
		if rErr != nil {
			return nil, fmt.Errorf("read %s/%s: %w", sk.Name, rel, rErr)
		}
		part, pErr := renderOne(string(raw), sk.Name+"/"+rel, true)
		if pErr != nil {
			return nil, pErr
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func verifySkillDiscoveryAndPreloads(
	ctx context.Context,
	injRenderer *prompts.InjectRenderer,
	label, profileID string,
	auth hoststate.TurnAuthority,
	admitted []skills.Skill,
	renderedBody map[string]string,
	audit *SkillSurfaceAudit,
) {
	surface := prompts.AgentPromptSurface{FullCatalog: true}
	admittedSet := make(map[string]bool, len(admitted))
	for _, sk := range admitted {
		admittedSet[sk.Name] = true
		surface.Skills = append(surface.Skills, prompts.AgentSkillView{
			Name:        sk.Name,
			Description: strings.TrimSpace(sk.Description),
		})
	}
	occ := prompts.AgentPromptSurfaceTemplateVars(surface)
	merged, err := prompts.Vars(auth.Reader, occ)
	if err != nil {
		audit.Violations = append(audit.Violations, fmt.Sprintf("%s skill discovery vars: %v", label, err))
		return
	}
	audit.DiscoveryChecks++
	hasSkillsRead := slices.Contains(auth.Offered, "skills_read")
	rows, _ := merged["agent_skills"].([]map[string]any)
	if !hasSkillsRead && len(rows) > 0 {
		audit.Violations = append(audit.Violations, fmt.Sprintf("%s retained agent_skills listing without skills_read", label))
	}
	if hasSkillsRead && len(rows) != len(admitted) {
		audit.Violations = append(audit.Violations, fmt.Sprintf("%s discovery listing has %d skills, want %d", label, len(rows), len(admitted)))
	}
	for _, row := range rows {
		name, _ := row["name"].(string)
		if !admittedSet[name] {
			audit.Violations = append(audit.Violations, fmt.Sprintf("%s discovery listing includes excluded skill %q", label, name))
		}
	}

	if len(admitted) == 0 || injRenderer == nil {
		return
	}
	// Exercise both resource-bearing and resource-free skill preloads + skill pointers when skills_read is offered.
	for _, sk := range admitted {
		if len(sk.Resources) > 0 || sk.Name == admitted[0].Name {
			bodyText := renderedBody[sk.Name]
			if bodyText == "" {
				continue
			}
			triggerTool := "command"
			if len(auth.Offered) > 0 {
				triggerTool = auth.Offered[0]
			}
			procBlock, procErr := inject.RenderSkillProcedureBlock(ctx, injRenderer, auth.Reader, "sess-skill", profileID, turnload.SkillPreload{
				Name:      sk.Name,
				Body:      bodyText,
				Tool:      triggerTool,
				Resources: sk.Resources,
			})
			if procErr != nil {
				audit.Violations = append(audit.Violations, fmt.Sprintf("%s RenderSkillProcedureBlock(%s): %v", label, sk.Name, procErr))
			} else if strings.TrimSpace(procBlock) != "" {
				audit.PreloadProcedureChecks++
			}
			if hasSkillsRead {
				ptrBlock, ptrErr := inject.RenderSkillPointerBlock(ctx, injRenderer, auth.Reader, "sess-skill", profileID, turnload.SkillRank{Name: sk.Name})
				if ptrErr != nil {
					audit.Violations = append(audit.Violations, fmt.Sprintf("%s RenderSkillPointerBlock(%s): %v", label, sk.Name, ptrErr))
				} else if strings.TrimSpace(ptrBlock) != "" {
					audit.PreloadPointerChecks++
				}
			}
			if len(sk.Resources) > 0 {
				break
			}
		}
	}
}

func verifyWarmColdSkillSchemaNarrowing(ctx context.Context, stock *Stock, loaded []skills.Skill, audit *SkillSurfaceAudit) error {
	var secretSkill, hostResSkill *skills.Skill
	for i := range loaded {
		if loaded[i].Name == "use-secrets-without-reading-them" {
			secretSkill = &loaded[i]
		}
		if loaded[i].Name == "work-with-github" {
			hostResSkill = &loaded[i]
		}
	}
	askMeta, ok := stock.Schemas.ToolMeta("ask_user")
	if !ok {
		return fmt.Errorf("missing ask_user schema")
	}
	verifyMeta, ok := stock.Schemas.ToolMeta("verify")
	if !ok {
		return fmt.Errorf("missing verify schema")
	}
	cmdMeta, ok := stock.Schemas.ToolMeta("command")
	if !ok {
		return fmt.Errorf("missing command schema")
	}
	wideAsk := toolschema.ProjectToolArgsSchema("ask_user", askMeta.ArgsSchema, toolschema.ProjectOptions{AskMode: false})
	narrowAsk := toolschema.ProjectToolArgsSchema("ask_user", askMeta.ArgsSchema, toolschema.ProjectOptions{AskMode: true})
	wideVerify := toolschema.ProjectToolArgsSchema("verify", verifyMeta.ArgsSchema, toolschema.ProjectOptions{})
	narrowVerify := map[string]any{"type": "object", "properties": map[string]any{"socks_proxy": map[string]any{"type": "boolean"}}}
	wideCmd := toolschema.ProjectToolArgsSchema("command", cmdMeta.ArgsSchema, toolschema.ProjectOptions{CommandReadOnly: false})
	narrowCmd := toolschema.ProjectToolArgsSchema("command", cmdMeta.ArgsSchema, toolschema.ProjectOptions{CommandReadOnly: true})

	prof := sandbox.ToolProfile{
		ID:             "narrow_probe",
		Tools:          map[string]bool{"ask_user": true, "verify": true, "command": true, "secret_generate": true, "secret_list": true},
		CommandEffects: toolcontract.CommandEffects(0xFF),
	}
	offered := []string{"ask_user", "command", "secret_generate", "secret_list", "verify"}
	loadedMap := map[string]bool{"ask_user": true, "command": true, "secret_generate": true, "secret_list": true, "verify": true}

	buildReader := func(wide bool) (prompts.Reader, error) {
		proj := map[string]map[string]any{"ask_user": narrowAsk, "verify": narrowVerify, "command": narrowCmd}
		if wide {
			proj = map[string]map[string]any{"ask_user": wideAsk, "verify": wideVerify, "command": wideCmd}
		}
		return prompts.NewReader(prompts.HostFacts{
			Profiles:  []sandbox.ToolProfile{prof},
			Schemas:   stock.Schemas,
			RootCount: 1,
			HasGit:    true,
			WebSearch: true,
		}, prompts.TurnProjection{
			ProfileID:        "narrow_probe",
			Offered:          readerDeclaredOffer([]sandbox.ToolProfile{prof}, "narrow_probe", offered, loadedMap),
			ProjectedSchemas: proj,
			Declared:         true,
		})
	}
	wideR, err := buildReader(true)
	if err != nil {
		return err
	}
	narrowR, err := buildReader(false)
	if err != nil {
		return err
	}
	for _, wideFirst := range []bool{true, false} {
		seq := []struct {
			wide bool
			r    prompts.Reader
		}{{wideFirst, wideR}, {!wideFirst, narrowR}}
		if !wideFirst {
			seq = []struct {
				wide bool
				r    prompts.Reader
			}{{false, narrowR}, {true, wideR}}
		}
		for _, step := range seq {
			vars, vErr := prompts.SkillVars(step.r)
			if vErr != nil {
				return vErr
			}
			if got, _ := vars["can_ask_user_secret"].(bool); got != step.wide {
				audit.Violations = append(audit.Violations, fmt.Sprintf("warm/cold schema narrowing (wideFirst=%t, wide=%t): can_ask_user_secret=%t", wideFirst, step.wide, got))
			}
			if hostResSkill != nil {
				if got := toolvocab.SkillToolsOfferedForReader(*hostResSkill, step.r); got != step.wide {
					audit.Violations = append(audit.Violations, fmt.Sprintf("warm/cold host_resources skill %s (wideFirst=%t, wide=%t): offered=%t", hostResSkill.Name, wideFirst, step.wide, got))
				}
			}
			if secretSkill != nil {
				parts, rErr := RenderSkill(ctx, *secretSkill, vars)
				if rErr != nil {
					return rErr
				}
				joined := parts[0].Text
				if !step.wide && strings.Contains(joined, "response_type: secret") {
					audit.Violations = append(audit.Violations, fmt.Sprintf("narrowed ask_user render leaked response_type: secret (wideFirst=%t)", wideFirst))
				}
			}
			audit.WarmColdNarrowingChecks++
		}
	}
	return nil
}

// StockSkills loads the stock pack skills with their bundled resources; a
// skill diagnostic fails the load.
func StockSkills(stock *Stock) ([]skills.Skill, error) {
	loaded, diags := extpacks.LoadEffectiveSkills(stock.Catalog)
	if len(diags) > 0 {
		messages := make([]string, 0, len(diags))
		for _, diag := range diags {
			messages = append(messages, diag.UnitID+": "+diag.Message)
		}
		return nil, fmt.Errorf("stock skills carry diagnostics:\n%s", strings.Join(messages, "\n"))
	}
	if len(loaded) == 0 {
		return nil, fmt.Errorf("no stock skills loaded")
	}
	return loaded, nil
}

// RenderSkill renders a skill body and each bundled markdown resource the way
// a read delivers them, with the empty configuration map production keeps
// for packs without settings.
func RenderSkill(ctx context.Context, sk skills.Skill, vars map[string]any) ([]Text, error) {
	return renderSkillCached(ctx, hoststate.NewRenderCache(), "", sk, vars)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
