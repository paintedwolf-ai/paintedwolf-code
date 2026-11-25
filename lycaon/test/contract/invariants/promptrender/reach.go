package promptrender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// reachInvariants verify that rendered guidance names only the tools,
// command programs, schema fields, and subjects its reader is offered.
var reachInvariants = []invariants.Invariant{
	{Name: "coordinator-kicks-name-only-offered-tools", Anchors: []invariants.Anchor{kickRenderer}, Check: coordinatorKicksNameOnlyOffered},
	{Name: "phase-kicks-name-only-their-surface-tools", Anchors: []invariants.Anchor{kickRenderer, phaseSurfaceResolver}, Check: phaseKicksNameOnlyTheirSurface},
	{Name: "worker-kicks-name-only-offered-tools", Anchors: []invariants.Anchor{kickRenderer}, Check: workerKicksNameOnlyOffered},
	{Name: "worker-personas-name-only-offered-tools", Anchors: []invariants.Anchor{personaRenderer}, Check: workerPersonasNameOnlyOffered},
	{Name: "skills-name-only-offered-tools", Anchors: []invariants.Anchor{invariants.Func("internal/toolvocab", "SkillToolsOffered"), invariants.Func("internal/toolvocab", "SkillToolsOfferedForReader")}, Check: skillsNameOnlyOffered},
	{Name: "reject-copy-names-only-schema-fields", Anchors: []invariants.Anchor{invariants.Func("internal/guidance", "NewStaticRejectFormatter")}, Check: rejectCopyNamesOnlySchemaFields},
	{Name: "phase-exits-are-callable-on-their-surface", Anchors: []invariants.Anchor{phaseSurfaceResolver, invariants.Func("internal/workflow/presentation", "ProjectPhaseExit")}, Check: phaseExitsAreCallable},
}

var phaseSurfaceResolver = invariants.Func("internal/workflow/definition", "ResolveSurfaceBinding")

var phaseKickPrefixes = []string{
	"coordinator-debate-", "coordinator-fanout-", "coordinator-phase-exit", "coordinator-plan-", "coordinator-recon-",
	"coordinator-review-loop-", "coordinator-security-",
}

type memoizedClaimResult struct {
	viols []toolvocabtest.SubjectViolation
	diags []toolvocabtest.BacktickSpan
}

var (
	claimEvalMemo       sync.Map
	claimAuthKeyMemo    sync.Map
	workerOccByReaderFP sync.Map
)

func semanticClaimAuthorityKey(auth hoststate.TurnAuthority) string {
	rawKey := auth.Digest + "|" + auth.Reader.Fingerprint()
	if cached, ok := claimAuthKeyMemo.Load(rawKey); ok {
		return cached.(string)
	}
	postKey := hoststate.RenderCacheKey("claim-postload", "", nil, map[string]any{
		"post": auth.PostLoadSchemas,
	})
	sem := rawKey + "|" + postKey
	claimAuthKeyMemo.Store(rawKey, sem)
	return sem
}

func isAuthoringMetaDoc(rel string) bool {
	v, err := toolvocabtest.StockBacktickVocab()
	return err == nil && v.IsAuthoringReferenceSource(rel)
}

func labelMemoClass(label string) string {
	if isAuthoringMetaDoc(label) {
		return "meta"
	}
	return "std"
}

func checkClaimsOnce(
	s *Stock,
	label, text string,
	auth hoststate.TurnAuthority,
	mode api.SessionMode,
	posture api.SessionPosture,
	subject toolvocabtest.ClaimSubject,
	followup *toolvocabtest.ActorAuthority,
	nextTurn *toolvocabtest.ActorAuthority,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
	checkBackticks bool,
) []string {
	var memoKey string
	if auth.Digest != "" && followup == nil && nextTurn == nil {
		sum := sha256.Sum256([]byte(text))
		rev := ""
		if s.Catalog != nil {
			rev = s.Catalog.Revision
		}
		memoKey = fmt.Sprintf(
			"%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%t|%t",
			hex.EncodeToString(sum[:]), s.Site, rev, semanticClaimAuthorityKey(auth),
			labelMemoClass(label), mode, posture, subject.Kind, subject.Modality, subject.AgentID,
			checkBackticks, resolveNamed != nil,
		)
		if cached, ok := claimEvalMemo.Load(memoKey); ok {
			res := cached.(memoizedClaimResult)
			return formatClaimViolations(label, text, res.viols, res.diags)
		}
	}
	evalCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:           s.Names,
		Prunable:          s.Prunable,
		Vocab:             s.Vocab,
		Self:              auth.ActorAuthority(),
		SessionMode:       mode,
		Posture:           posture,
		FollowupLeg:       followup,
		SessionNextTurn:   nextTurn,
		ResolveNamedAgent: resolveNamed,
	}
	viols := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{
		Label:   label,
		Subject: subject,
		Text:    text,
	})
	var diags []toolvocabtest.BacktickSpan
	if checkBackticks && s.Vocab != nil && !isAuthoringMetaDoc(label) {
		diags = s.Vocab.CheckProse(label, text)
	}
	if memoKey != "" {
		claimEvalMemo.Store(memoKey, memoizedClaimResult{viols: viols, diags: diags})
	}
	return formatClaimViolations(label, text, viols, diags)
}

func formatClaimViolations(
	label, text string,
	viols []toolvocabtest.SubjectViolation,
	diags []toolvocabtest.BacktickSpan,
) []string {
	var msgs []string
	for _, v := range viols {
		msgs = append(msgs, fmt.Sprintf("%s names `%s` (%s): %s", label, v.Claim, v.Reason, lineNaming(text, v.Claim)))
	}
	for _, diag := range diags {
		msgs = append(msgs, fmt.Sprintf("%s unclassified protocol backtick `%s` at line %d", label, diag.Span, diag.Line))
	}
	return msgs
}

func lineNaming(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "`"+name+"`") || strings.Contains(line, "`"+name+"(") || strings.Contains(line, name) {
			return fmt.Sprintf("%q", strings.TrimSpace(line))
		}
	}
	return "(no single line)"
}

func kickData() map[string]any {
	data := KickFixture()
	delete(data, "evidence_digest")
	return data
}

func informKickIDs(reg *anchor.Registry, prefix string, extra ...string) []string {
	var ids []string
	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() || slices.Contains(ids, b.Render) {
			continue
		}
		if strings.HasPrefix(b.Render, prefix) || slices.Contains(extra, b.Render) {
			ids = append(ids, b.Render)
		}
	}
	slices.Sort(ids)
	return ids
}

var (
	cachedCoordinatorPairs = sync.OnceValues(func() ([]coordRep, error) {
		ctx := context.Background()
		states, err := hoststate.CoordinatorStates(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]coordRep, 0, len(states))
		for _, st := range states {
			if st.Excluded {
				continue
			}
			auth, fErr := hoststate.FreezeTurnAuthority(ctx, st)
			if fErr != nil {
				return nil, fErr
			}
			out = append(out, coordRep{state: st, auth: auth})
		}
		return out, nil
	})
	cachedCoordinatorReps = sync.OnceValues(func() ([]coordRep, error) {
		pairs, err := cachedCoordinatorPairs()
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var out []coordRep
		for _, p := range pairs {
			if !seen[p.auth.Digest] {
				seen[p.auth.Digest] = true
				out = append(out, p)
			}
		}
		return out, nil
	})
	cachedWorkerPairs = sync.OnceValues(func() ([]workerRep, error) {
		ctx := context.Background()
		workerStates, err := hoststate.WorkerStates(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]workerRep, 0, len(workerStates))
		for _, ws := range workerStates {
			if ws.Excluded {
				continue
			}
			auth, fErr := hoststate.FreezeWorkerAuthority(ctx, ws)
			if fErr != nil {
				return nil, fErr
			}
			out = append(out, workerRep{ws: ws, auth: auth})
		}
		return out, nil
	})
	cachedWorkerReps = sync.OnceValues(func() ([]workerRep, error) {
		pairs, err := cachedWorkerPairs()
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var out []workerRep
		for _, p := range pairs {
			if !seen[p.auth.Digest] {
				seen[p.auth.Digest] = true
				out = append(out, p)
			}
		}
		return out, nil
	})
)

// coordinatorKicksNameOnlyOffered verifies every non-phase coordinator kick
// across the coordinator host states and anchor envelopes.
func coordinatorKicksNameOnlyOffered(r *invariants.Run) {
	reps, err := cachedCoordinatorReps()
	r.Must(err)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	r.Must(err)
	sweep := newKickSweep(stock(r), hoststate.NewRenderCache(), func(v string) { r.Report("%s", v) })
	sweep.envelopes = canonicalKickEnvelope
	r.Must(sweep.coordinatorKicks(context.Background(), anchorReg, informKickIDs(anchorReg, "coordinator-", "session-agent-enabled"), reps))
	r.Expect(sweep.rendered, "coordinator kick renders")
}

// canonicalKickEnvelope is the one fixture the reach invariants render for
// every recipient; the host-state matrix renders the anchor and occurrence
// variants.
func canonicalKickEnvelope(reg *anchor.Registry, kick string) []map[string]any {
	return kickEnvelopeVariants(reg, kick)[:1]
}

func kickEnvelopeVariants(reg *anchor.Registry, kick string) []map[string]any {
	base := kickData()
	delete(base, "tools")
	out := []map[string]any{base}
	if reg == nil {
		return out
	}
	if aID, ok := reg.LookupRender(kick); ok {
		if envs, err := hoststate.AnchorEnvelopes(string(aID)); err == nil {
			for _, env := range envs {
				clean := stripReaderFactKeys(env)
				delete(clean, "evidence_digest")
				delete(clean, "tools")
				out = append(out, clean)
			}
		}
	}
	return out
}

func stripReaderFactKeys(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if !prompts.IsDeclaredOrReservedReaderFact(k) {
			out[k] = v
		}
	}
	return out
}

func closeoutTurnReader(st hoststate.CoordinatorState, worker bool) (prompts.Reader, error) {
	narrowed, err := hoststate.NarrowProfilesForPosture(st.Overlay.Profiles, st.Posture, false)
	if err != nil {
		return prompts.Reader{}, err
	}
	visible := []string{}
	if worker {
		visible = []string{"complete_leg"}
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SessionMode:           string(st.SessionMode),
		Profiles:              narrowed,
		Visible:               visible,
		Schemas:               st.Overlay.Schemas,
		RootCount:             st.RootCount,
		HasGit:                st.HasGit,
		HasAnyGitRoot:         st.HasAnyGitRoot,
		WebSearch:             st.WebSearch,
		CommandExternalHidden: st.CommandExternalHidden,
		SessionScratchDir:     st.SessionScratchDir,
		SharedTempLocations:   st.SharedTempLocations,
	})
}

// phaseKicksNameOnlyTheirSurface verifies workflow phase.entered kicks across
// the coordinator host states of each phase's surface.
func phaseKicksNameOnlyTheirSurface(r *invariants.Run) {
	reg, err := workflowdef.RegistryFromDirs("")
	r.Must(err)
	pairs, err := cachedCoordinatorPairs()
	r.Must(err)
	sweep := newKickSweep(stock(r), hoststate.NewRenderCache(), func(v string) { r.Report("%s", v) })
	r.Must(sweep.phaseKicks(context.Background(), reg, pairs))
	r.Expect(sweep.rendered, "phase kick renders")
}

// workerKicksNameOnlyOffered verifies worker kicks across the worker host
// states and anchor envelopes, including the follow-up leg on worker-closeout.
func workerKicksNameOnlyOffered(r *invariants.Run) {
	reps, err := cachedWorkerReps()
	r.Must(err)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	r.Must(err)
	sweep := newKickSweep(stock(r), hoststate.NewRenderCache(), func(v string) { r.Report("%s", v) })
	sweep.envelopes = canonicalKickEnvelope
	r.Must(sweep.workerKicks(context.Background(), anchorReg, informKickIDs(anchorReg, "worker-"), reps))
	r.Expect(sweep.rendered, "worker kick renders")
}

func workerCloseoutReader(ws hoststate.WorkerState) (prompts.Reader, error) {
	askChild := ws.ParentSessionMode == api.SessionModeAsk
	narrowed, err := hoststate.NarrowProfilesForPosture(ws.Overlay.Profiles, ws.Posture, askChild)
	if err != nil {
		return prompts.Reader{}, err
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID:             ws.ToolProfile,
		SessionMode:           string(ws.ParentSessionMode),
		Profiles:              narrowed,
		Visible:               []string{"complete_leg"},
		Schemas:               ws.Overlay.Schemas,
		RootCount:             ws.RootCount,
		HasGit:                ws.HasGit,
		HasAnyGitRoot:         ws.HasAnyGitRoot,
		OnWorkerBranch:        ws.OnWorkerBranch,
		WebSearch:             ws.WebSearch,
		CommandExternalHidden: ws.CommandExternalHidden,
	})
}

// workerPersonasNameOnlyOffered verifies every worker persona across hoststate.WorkerStates.
func workerPersonasNameOnlyOffered(r *invariants.Run) {
	ctx := context.Background()
	s := stock(r)
	engine := s.Engine()
	cache := hoststate.NewRenderCache()
	workerPairs, err := cachedWorkerPairs()
	r.Must(err)
	skillsList, err := StockSkills(s)
	r.Must(err)

	checked := 0
	for _, wp := range workerPairs {
		ws, auth := wp.ws, wp.auth
		if ws.ParentSessionMode == api.SessionModeAsk {
			if mutCapable, ok := prompts.AgentMutationCapable(ws.AgentID); ok && mutCapable {
				continue
			}
		}
		occ := workerPersonaOccurrence(auth.Reader, skillsList)
		fullVars, err := prompts.Vars(auth.Reader, stripReaderFactKeys(occ))
		r.Must(err)
		rKey := renderCacheKeyForVars("persona:"+ws.AgentID+":"+s.Site, s.Catalog.Revision, nil, fullVars)
		text, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
			return prompts.RenderPersona(ctx, engine, ws.AgentID, auth.Reader, occ)
		})
		r.Must(rErr)
		checked++
		label := fmt.Sprintf("persona %s (%s)", ws.AgentID, ws.ID)
		checkClaimsCached(cache, rKey, auth.Digest, func(v string) { r.Report("%s", v) }, func() []string {
			return checkClaimsOnce(s, label, text, auth, ws.ParentSessionMode, ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
		})
	}
	r.Expect(checked, "worker persona renders")
}

func workerPersonaOccurrence(reader prompts.Reader, skillsList []skills.Skill) map[string]any {
	fp := reader.Fingerprint()
	if fp != "" {
		if cached, ok := workerOccByReaderFP.Load(fp); ok {
			return maps.Clone(cached.(map[string]any))
		}
	}
	occ := cloneBaseSpawnVars()
	var views []prompts.AgentSkillView
	for _, sk := range skillsList {
		if !sk.Project && toolvocab.SkillToolsOfferedForReader(sk, reader) {
			views = append(views, prompts.AgentSkillView{
				Name:        sk.Name,
				Description: strings.TrimSpace(sk.Description),
			})
		}
	}
	maps.Copy(occ, prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
		FullCatalog: true,
		Skills:      views,
	}))
	if fp != "" {
		workerOccByReaderFP.Store(fp, maps.Clone(occ))
	}
	return occ
}

// skillsNameOnlyOffered verifies every rendered stock skill across CoordinatorStates
// and WorkerStates through EvaluateClaimBlock and closed backtick vocabulary.
func skillsNameOnlyOffered(r *invariants.Run) {
	s := stock(r)
	renders := skillRenders(r)
	r.Expect(len(renders), "stock skill renders")
	cache := hoststate.NewRenderCache()
	for _, sr := range renders {
		texts := make([]string, len(sr.Parts))
		for i, part := range sr.Parts {
			texts[i] = part.Text
		}
		joined := strings.Join(texts, "\n")
		label := fmt.Sprintf("skill %s as read by %s", sr.Skill.Name, sr.Reader)
		rKey := hoststate.RenderCacheKey("skillcheck:"+sr.Skill.Name, s.Catalog.Revision, nil, map[string]any{"body": joined})
		checkClaimsCached(cache, rKey, sr.Authority.Digest, func(v string) { r.Report("%s", v) }, func() []string {
			auth := sr.Authority
			if !auth.Reader.Valid() {
				auth.Reader = sr.State
			}
			return checkClaimsOnce(s, label, joined, auth, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
		})
	}
}

var backtickedIdent = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*)`")

type fieldIndex struct {
	byTool map[string]map[string]bool
	union  map[string]bool
}

func newFieldIndex(schemas *toolschema.Config) fieldIndex {
	idx := fieldIndex{byTool: map[string]map[string]bool{}, union: map[string]bool{}}
	for name := range schemas.Tools {
		meta, ok := schemas.ToolMeta(name)
		if !ok {
			continue
		}
		fields := map[string]bool{}
		for _, path := range toolschema.ArgFieldPaths(meta.ArgsSchema) {
			head, _, _ := strings.Cut(path, ".")
			for _, p := range []string{path, head} {
				fields[p] = true
				idx.union[p] = true
			}
		}
		idx.byTool[name] = fields
	}
	return idx
}

func (idx fieldIndex) unschemaed(tool, text string) []string {
	named := map[string]bool{tool: true}
	var claims []string
	for _, m := range backtickedIdent.FindAllStringSubmatch(text, -1) {
		head, _, _ := strings.Cut(m[1], ".")
		if _, isTool := idx.byTool[head]; isTool {
			named[head] = true
		} else if idx.union[m[1]] {
			claims = append(claims, m[1])
		}
	}
	var bad []string
	for _, claim := range claims {
		if !slices.ContainsFunc(sortedKeys(named), func(t string) bool { return idx.byTool[t][claim] }) && !slices.Contains(bad, claim) {
			bad = append(bad, claim)
		}
	}
	slices.Sort(bad)
	return bad
}

func rejectCopyNamesOnlySchemaFields(r *invariants.Run) {
	idx := newFieldIndex(stock(r).Schemas)
	cfg := invariants.HintConfig(r.Corpus)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(stock(r).Engine()))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	checked := 0
	for _, code := range sortedKeys(cfg.HintCodes) {
		entry := cfg.HintCodes[code]
		for _, tool := range entry.Tools {
			tool = strings.TrimSpace(tool)
			if _, ok := idx.byTool[tool]; !ok {
				continue
			}
			checked++
			texts := []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead}
			for _, sc := range entry.Scenarios {
				vars := guidance.ScenarioVars(entry, sc)
				vars["tool"] = tool
				if rendered, err := formatter.Format(code, vars); err == nil {
					texts = append(texts, rendered)
				}
			}
			for _, field := range idx.unschemaed(tool, strings.Join(texts, "\n")) {
				r.Report("%s rendered for %s names `%s`, which %s does not accept", code, tool, field, tool)
			}
		}
	}
	r.Expect(checked, "rejection cards with a tool selector")
}

var phaseExitCall = regexp.MustCompile("((?i:do not |never ))?call `([a-z_]+)`")

func phaseExitsAreCallable(r *invariants.Run) {
	s := stock(r)
	reg, err := workflowdef.RegistryFromDirs("")
	r.Must(err)
	plans, err := surface.CompileToolPlans(1)
	r.Must(err)
	coordinator := slices.IndexFunc(s.Profiles, func(p sandbox.ToolProfile) bool { return p.ID == prompts.CoordinatorProfileID })
	if coordinator < 0 {
		r.Fatalf("the coordinator tool profile is not declared")
	}
	grants := s.Profiles[coordinator].Tools
	engine := s.Engine()
	checked := 0
	for _, key := range sortedKeys(reg.All()) {
		m := reg.All()[key]
		for _, phase := range m.PhaseDefs {
			id := strings.TrimSpace(phase.CoordinatorSurface)
			if id == "" {
				binding, err := workflowdef.ResolveSurfaceBinding(m, phase.ID, m.ID)
				r.Must(err)
				id = strings.TrimSpace(binding.CoordinatorSurface)
			}
			plan, ok := plans[id]
			if !ok {
				continue
			}
			checked++
			wire := plan.AddressableNames()
			label := fmt.Sprintf("%s phase %s on %s", key, phase.ID, id)
			for _, channel := range phaseExitChannels(m, phase) {
				if !slices.Contains(wire, channel.tool) {
					r.Report("%s needs %s (%s), which that surface does not offer", label, channel.tool, channel.why)
				}
			}
			vars := inject.ActiveWorkflowInjectToMap(inject.ActiveWorkflowInjectData{
				WorkflowID: "invariants", CurrentPhase: "p",
				PhaseExit: workflowpresentation.ProjectPhaseExit(m, phase, nil, nil).InjectView(),
				Phases:    []inject.ActiveWorkflowPhaseView{{ID: "p", IsCurrent: true}},
			}, nil, nil)
			text, err := engine.Render(context.Background(), "guidance/active-workflow.md", vars)
			r.Must(err)
			seen := map[string]bool{}
			for _, match := range phaseExitCall.FindAllStringSubmatch(text, -1) {
				name := match[2]
				if match[1] != "" || seen[name] || !grants[name] || slices.Contains(wire, name) {
					continue
				}
				seen[name] = true
				r.Report("%s exit step calls `%s`, which that surface does not offer", label, name)
			}
		}
	}
	r.Expect(checked, "workflow phases bound to a surface")
}

type exitChannel struct{ tool, why string }

func phaseExitChannels(m workflowdef.Manifest, phase workflowdef.PhaseDef) []exitChannel {
	if phase.Terminal {
		return nil
	}
	var out []exitChannel
	if workflowdef.EffectiveAdvancePolicy(m, phase) == workflowdef.AdvanceWhenGateMetCoordinator {
		out = append(out, exitChannel{"workflow_advance", "advance.when_gate_met: coordinator"})
	}
	if phase.ReviewLoop != nil {
		out = append(out, exitChannel{"submit_verdict", "review_loop verdict"})
	}
	if slices.ContainsFunc(phase.Gates, func(g string) bool { return strings.HasPrefix(g, "hitl_consulted") }) {
		out = append(out, exitChannel{"ask_user", "a hitl_consulted gate"})
	}
	for _, tr := range phase.Transitions {
		if slices.Contains(tr.Actors, workflowdef.TransitionActorCoordinator) {
			out = append(out, exitChannel{"workflow_transition", "coordinator transition " + tr.ID})
		}
	}
	return out
}
