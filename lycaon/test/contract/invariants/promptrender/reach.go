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
	"github.com/lycaon/lycaon/internal/tools"
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

// evaluateAndReportClaims checks text against auth via EvaluateClaimBlock
// and closed backtick vocabulary, reporting violations onto r.
func evaluateAndReportClaims(
	r *invariants.Run,
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
) {
	if violations, err := checkClaimsOnce(s, label, text, auth, mode, posture, subject, followup, nextTurn, resolveNamed, checkBackticks); err != nil {
		for _, v := range violations {
			r.Report("%s", v)
		}
	}
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
) ([]string, error) {
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
) ([]string, error) {
	if len(viols) == 0 && len(diags) == 0 {
		return nil, nil
	}
	var msgs []string
	for _, v := range viols {
		msgs = append(msgs, fmt.Sprintf("%s names `%s` (%s): %s", label, v.Claim, v.Reason, lineNaming(text, v.Claim)))
	}
	for _, diag := range diags {
		msgs = append(msgs, fmt.Sprintf("%s unclassified protocol backtick `%s` at line %d", label, diag.Span, diag.Line))
	}
	return msgs, fmt.Errorf("%d claim violations", len(msgs))
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

func informKicks(r *invariants.Run, prefix string) []string {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	r.Must(err)
	var ids []string
	for _, b := range reg.AllInform() {
		if b != nil && b.IsInform() && strings.HasPrefix(b.Render, prefix) && !slices.Contains(ids, b.Render) {
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

func alignKickOccurrenceToAuthority(kick string, data map[string]any, reader prompts.Reader, offered []string) {
	if kick == "coordinator-progress-missing" {
		if rawTool, ok := data["tool"].(string); ok && rawTool != "" && !slices.Contains(offered, rawTool) {
			if len(offered) > 0 {
				data["tool"] = offered[0]
			} else {
				delete(data, "tool")
			}
		}
	}
	if rawTools, ok := data["tools"].([]string); ok && len(rawTools) > 0 {
		var kept []string
		for _, t := range rawTools {
			if slices.Contains(offered, t) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 && len(offered) > 0 {
			kept = []string{offered[0]}
		}
		if len(kept) > 0 {
			data["tools"] = kept
		} else {
			delete(data, "tools")
		}
	}
	if adv, ok := data["advance_when_gate_met"].(string); ok && adv == "coordinator" && !slices.Contains(offered, "workflow_advance") {
		data["advance_when_gate_met"] = "host"
	}
	mints := reader.Mints()
	if !mints["web"] {
		if targets, ok := data["repair_targets"].([]map[string]any); ok {
			cleaned := make([]map[string]any, len(targets))
			for i, t := range targets {
				cp := maps.Clone(t)
				delete(cp, "candidates")
				delete(cp, "use")
				cleaned[i] = cp
			}
			data["repair_targets"] = cleaned
		}
	}
	if !mints["read"] {
		delete(data, "evidence_digest")
		if _, hasIssues := data["document_issues"]; hasIssues {
			data["document_issues"] = []map[string]any{{"message": "bad citation", "offenders": []string{"ungrounded-ref"}}}
		}
		if _, hasOff := data["offenders_sample"]; hasOff {
			data["offenders_sample"] = []string{"ungrounded-ref"}
		}
	}
}

// coordinatorKicksNameOnlyOffered verifies every non-phase coordinator kick
// across hoststate.CoordinatorStates and AnchorEnvelopes.
func coordinatorKicksNameOnlyOffered(r *invariants.Run) {
	ctx := context.Background()
	s := stock(r)
	engine := s.Engine()
	cache := hoststate.NewRenderCache()
	reps, err := cachedCoordinatorReps()
	r.Must(err)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	r.Must(err)

	checked := 0
	kicks := informKicks(r, "coordinator-")
	for _, kick := range kicks {
		if slices.ContainsFunc(phaseKickPrefixes, func(p string) bool { return strings.HasPrefix(kick, p) }) {
			continue
		}
		envelopes := kickEnvelopeVariants(anchorReg, kick)
		for envIdx, envBase := range envelopes {
			// Sweep canonical fixture (envIdx == 0) across all distinct authorities,
			// and additional AnchorEnvelopes boundaries across representative authorities.
			limit := len(reps)
			if envIdx > 0 && limit > 8 {
				limit = 8
			}
			for i := 0; i < limit; i++ {
				sa := reps[i]
				if kick == "coordinator-closeout" && !surface.SurfaceDeliversReport(sa.state.TurnProfile.SurfaceID) {
					continue
				}
				reader := sa.auth.Reader
				subj := toolvocabtest.SubjectSelfNow()
				var nextTurn *toolvocabtest.ActorAuthority
				data := maps.Clone(envBase)
				alignKickOccurrenceToAuthority(kick, data, sa.auth.Reader, sa.auth.Offered)
				if kick == "coordinator-turn-closeout" {
					// Turn-closeout strips current-turn tools and advises session_next_turn.
					closeReader, cErr := closeoutTurnReader(sa.state, false)
					r.Must(cErr)
					reader = closeReader
					activeActor := sa.auth.ActorAuthority()
					nextTurn = &activeActor
					subj = toolvocabtest.SubjectSessionNextTurn()
					data["session_command_mutation_capable"] = sa.state.SessionMode != api.SessionModeAsk && sa.auth.Reader.Held()["command_mutation"]
				}
				prompts.MergeCoordinatorKickVarsForTools(data, reader)
				fullVars, _ := prompts.Vars(reader, stripReaderFactKeys(data))
				rKey := renderCacheKeyForVars("kick:"+kick+":"+s.Site, s.Catalog.Revision, nil, fullVars)
				text, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
					return engine.RenderKick(ctx, reader, kick, data)
				})
				r.Must(rErr)
				checked++
				label := fmt.Sprintf("kick %s on %s (%s)", kick, sa.state.TurnProfile.SurfaceID, sa.state.ID)
				checkKey := sa.auth.Digest + "|" + string(subj.Kind)
				if cErr := cache.CheckAuthority(rKey, checkKey, func() error {
					targetAuth := sa.auth
					targetAuth.Reader = reader
					if subj.Kind == toolvocabtest.SubjectKindSessionNextTurn {
						targetAuth.Metas = nil
						targetAuth.Schemas = map[string]map[string]any{}
						targetAuth.Offered = nil
					}
					viols, err := checkClaimsOnce(s, label, text, targetAuth, sa.state.SessionMode, sa.state.Posture, subj, nil, nextTurn, nil, true)
					if err != nil {
						for _, msg := range viols {
							r.Report("%s", msg)
						}
					}
					return err
				}); cErr != nil {
					_ = cErr
				}
			}
		}
	}
	r.Expect(checked, "coordinator kick renders")
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
	return prompts.NewReader(prompts.HostFacts{
		SessionMode:           string(st.SessionMode),
		Profiles:              narrowed,
		Schemas:               st.Overlay.Schemas,
		RootCount:             st.RootCount,
		HasGit:                st.HasGit,
		HasAnyGitRoot:         st.HasAnyGitRoot,
		WebSearch:             st.WebSearch,
		CommandExternalHidden: st.CommandExternalHidden,
		SessionScratchDir:     st.SessionScratchDir,
		SharedTempLocations:   st.SharedTempLocations,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		Offered:   visible,
		Declared:  true,
	})
}

// phaseKicksNameOnlyTheirSurface verifies workflow phase.entered kicks across
// postures, modes, and loading states from hoststate.CoordinatorStates.
func phaseKicksNameOnlyTheirSurface(r *invariants.Run) {
	ctx := context.Background()
	s := stock(r)
	reg, err := workflowdef.RegistryFromDirs("")
	r.Must(err)
	pairs, err := cachedCoordinatorPairs()
	r.Must(err)
	engine := s.Engine()
	cache := hoststate.NewRenderCache()

	bySurface := map[string][]coordRep{}
	for _, cp := range pairs {
		if cp.state.PackOverlayID != "stock" {
			continue
		}
		bySurface[cp.state.TurnProfile.SurfaceID] = append(bySurface[cp.state.TurnProfile.SurfaceID], cp)
	}

	checked := 0
	for _, key := range sortedKeys(reg.All()) {
		m := reg.All()[key]
		surfaces := map[string]string{}
		for _, phase := range m.PhaseDefs {
			surfaces[phase.ID] = phase.CoordinatorSurface
		}
		for _, in := range m.Injects {
			if in.On != "phase.entered" || in.Selector.Phase == nil {
				continue
			}
			surfID := surfaces[*in.Selector.Phase]
			for _, cp := range bySurface[surfID] {
				st, auth := cp.state, cp.auth
				data := kickData()
				prompts.MergeCoordinatorKickVarsForTools(data, auth.Reader)
				fullVars, _ := prompts.Vars(auth.Reader, stripReaderFactKeys(data))
				rKey := renderCacheKeyForVars("phasekick:"+in.Render+":"+s.Site, s.Catalog.Revision, nil, fullVars)
				text, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
					return engine.RenderKick(ctx, auth.Reader, in.Render, data)
				})
				r.Must(rErr)
				checked++
				label := fmt.Sprintf("%s phase %s kick %s on %s (%s)", key, *in.Selector.Phase, in.Render, surfID, st.ID)
				_ = cache.CheckAuthority(rKey, auth.Digest, func() error {
					viols, cErr := checkClaimsOnce(s, label, text, auth, st.SessionMode, st.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
					for _, msg := range viols {
						r.Report("%s", msg)
					}
					return cErr
				})
			}
		}
	}
	r.Expect(checked, "phase kick renders")
}

// workerKicksNameOnlyOffered verifies worker-* kicks across hoststate.WorkerStates
// and AnchorEnvelopes, including SubjectFollowupLeg on worker-closeout.
func workerKicksNameOnlyOffered(r *invariants.Run) {
	ctx := context.Background()
	s := stock(r)
	engine := s.Engine()
	cache := hoststate.NewRenderCache()
	allReps, err := cachedWorkerReps()
	r.Must(err)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	r.Must(err)

	var reps []workerRep
	for _, wr := range allReps {
		if wr.auth.Reader.Held()["complete_leg"] {
			reps = append(reps, wr)
		}
	}

	checked := 0
	for _, kick := range informKicks(r, "worker-") {
		envelopes := kickEnvelopeVariants(anchorReg, kick)
		for envIdx, envBase := range envelopes {
			limit := len(reps)
			if envIdx > 0 && limit > 6 {
				limit = 6
			}
			for i := 0; i < limit; i++ {
				wa := reps[i]
				reader := wa.auth.Reader
				subj := toolvocabtest.SubjectSelfNow()
				var followup *toolvocabtest.ActorAuthority
				data := maps.Clone(envBase)
				if kick == "worker-closeout" || kick == "worker-cancel-closeout" {
					closeR, cErr := workerCloseoutReader(wa.ws)
					r.Must(cErr)
					reader = closeR
					if kick == "worker-closeout" {
						active := wa.auth.ActorAuthority()
						followup = &active
						subj = toolvocabtest.SubjectFollowupLeg()
						data["leg_command_mutation_capable"] = wa.ws.ParentSessionMode != api.SessionModeAsk && wa.auth.Reader.Held()["command_mutation"]
					}
				}
				fullVars, _ := prompts.Vars(reader, stripReaderFactKeys(data))
				rKey := renderCacheKeyForVars("wkick:"+kick+":"+s.Site, s.Catalog.Revision, nil, fullVars)
				text, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
					return engine.RenderKick(ctx, reader, kick, data)
				})
				r.Must(rErr)
				checked++
				label := fmt.Sprintf("kick %s for %s (%s)", kick, wa.ws.ToolProfile, wa.ws.ID)
				checkKey := wa.auth.Digest + "|" + string(subj.Kind)
				_ = cache.CheckAuthority(rKey, checkKey, func() error {
					targetAuth := wa.auth
					targetAuth.Reader = reader
					if kick == "worker-closeout" || kick == "worker-cancel-closeout" {
						var closeMetas []tools.ToolMeta
						for _, m := range wa.auth.Metas {
							if m.Name == "complete_leg" {
								closeMetas = append(closeMetas, m)
							}
						}
						targetAuth.Metas = closeMetas
						targetAuth.Schemas = map[string]map[string]any{}
						if len(closeMetas) > 0 {
							targetAuth.Schemas["complete_leg"] = closeMetas[0].ArgsSchema
						}
						targetAuth.Offered = []string{"complete_leg"}
					}
					viols, cErr := checkClaimsOnce(s, label, text, targetAuth, wa.ws.ParentSessionMode, wa.ws.Posture, subj, followup, nil, nil, true)
					for _, msg := range viols {
						r.Report("%s", msg)
					}
					return cErr
				})
			}
		}
	}
	r.Expect(checked, "worker kick renders")
}

func workerCloseoutReader(ws hoststate.WorkerState) (prompts.Reader, error) {
	askChild := ws.ParentSessionMode == api.SessionModeAsk
	narrowed, err := hoststate.NarrowProfilesForPosture(ws.Overlay.Profiles, ws.Posture, askChild)
	if err != nil {
		return prompts.Reader{}, err
	}
	return prompts.NewReader(prompts.HostFacts{
		SessionMode:           string(ws.ParentSessionMode),
		Profiles:              narrowed,
		Schemas:               ws.Overlay.Schemas,
		RootCount:             ws.RootCount,
		HasGit:                ws.HasGit,
		HasAnyGitRoot:         ws.HasAnyGitRoot,
		OnWorkerBranch:        ws.OnWorkerBranch,
		WebSearch:             ws.WebSearch,
		CommandExternalHidden: ws.CommandExternalHidden,
	}, prompts.TurnProjection{
		ProfileID: ws.ToolProfile,
		Offered:   []string{"complete_leg"},
		Declared:  true,
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
		fullVars, _ := prompts.Vars(auth.Reader, stripReaderFactKeys(occ))
		rKey := renderCacheKeyForVars("persona:"+ws.AgentID+":"+s.Site, s.Catalog.Revision, nil, fullVars)
		text, _, rErr := cache.RenderOrReuse(rKey, func() (string, error) {
			return prompts.RenderPersona(ctx, engine, ws.AgentID, auth.Reader, occ)
		})
		r.Must(rErr)
		checked++
		label := fmt.Sprintf("persona %s (%s)", ws.AgentID, ws.ID)
		_ = cache.CheckAuthority(rKey, auth.Digest, func() error {
			viols, cErr := checkClaimsOnce(s, label, text, auth, ws.ParentSessionMode, ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			for _, msg := range viols {
				r.Report("%s", msg)
			}
			return cErr
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
		_ = cache.CheckAuthority(rKey, sr.Authority.Digest, func() error {
			auth := sr.Authority
			if !auth.Reader.Valid() {
				auth.Reader = sr.State
			}
			viols, err := checkClaimsOnce(s, label, joined, auth, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
			for _, msg := range viols {
				r.Report("%s", msg)
			}
			return err
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
