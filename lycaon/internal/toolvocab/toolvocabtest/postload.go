package toolvocabtest

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/commandmode"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
)

func isBacktickAuthorizedByLoad(
	cat *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	auth ActorAuthority,
	offered map[string]bool,
	postMut bool,
	postReach toolreach.Request,
	sentence string,
	bs BacktickSpan,
	bindings []loadBinding,
) (authorized bool, schemaViolations []AttributedClaimIssue) {
	raw := strings.TrimSpace(bs.Span)
	for _, b := range bindings {
		if !isSpanSequencedWithLoader(sentence, bs.Start, bs.End, b) {
			continue
		}
		if raw == b.target {
			return true, nil
		}
		if rest, hasDot := strings.CutPrefix(raw, b.target+"."); hasDot {
			return validatePostLoadDottedSpan(b.target, rest, raw, bs.Line, auth)
		}
		if strings.HasPrefix(raw, b.target+"(") {
			return validatePostLoadCallSpan(cat, b.target, raw, bs.Line, auth, vocab, postMut, postReach)
		}
		if b.target == toolreach.ProgramTool && vocab != nil {
			if ok, iss, matched := validatePostLoadCommandSpan(raw, bs.Line, vocab, postMut, postReach); matched {
				return ok, iss
			}
		}
		if ok, iss, matched := validatePostLoadBareFieldSpan(cat, b.target, raw, bs.Line, auth, idx, offered); matched {
			return ok, iss
		}
	}
	return false, nil
}

func validatePostLoadDottedSpan(
	target, rest, raw string,
	line int,
	auth ActorAuthority,
) (bool, []AttributedClaimIssue) {
	p, val, hasVal, ell := splitPathAndOptionalValue(rest)
	norm := normalizeSchemaPath(p)
	projSchema, hasSchema := resolvePostLoadSchema(target, auth)
	if !hasSchema {
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: norm, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load schema for %q is uncertified; cannot verify %q", target, raw),
		}}
	}
	if !toolschema.SchemaAcceptsField(projSchema, norm) && !schemaAcceptsPath(projSchema, norm) {
		rep := missingPathHead(projSchema, norm)
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: rep, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load %s projected schema lacks %q", target, rep),
		}}
	}
	if hasVal && !ell {
		if iss := validateValueAtPath(target, projSchema, norm, val, line); len(iss) > 0 {
			return false, iss
		}
	}
	return true, nil
}

func validatePostLoadCallSpan(
	cat *toolvocab.Catalog,
	target, raw string,
	line int,
	auth ActorAuthority,
	vocab *BacktickVocab,
	postMut bool,
	postReach toolreach.Request,
) (bool, []AttributedClaimIssue) {
	calls := ParseCallExamples(cat, raw)
	if len(calls) == 0 {
		return false, nil
	}
	projSchema, hasSchema := resolvePostLoadSchema(target, auth)
	for _, ex := range calls {
		if ex.Tool != target {
			continue
		}
		if !hasSchema {
			return false, []AttributedClaimIssue{{
				Tool: target, AttributedTools: []string{target}, Path: target, Span: raw, Line: line,
				Reason: fmt.Sprintf("post-load schema for %q is uncertified; cannot verify call %q", target, raw),
			}}
		}
		if iss := validateCallAgainstSchema(ex, projSchema); len(iss) > 0 {
			return false, iss
		}
		if target == toolreach.ProgramTool {
			if cmdVal, hasCmd := ex.Args["command"]; hasCmd {
				if cmdStr, isConcrete := concreteStringOrIdent(cmdVal); isConcrete && !isPlaceholderText(cmdStr) {
					if ok, reason := checkAllCommandSegments(vocab, postReach, postMut, cmdStr); !ok {
						return false, []AttributedClaimIssue{{
							Tool: target, AttributedTools: []string{target}, Path: "command", Value: cmdStr, Span: raw, Line: line,
							Reason: reason,
						}}
					}
				}
			}
		}
	}
	return true, nil
}

func validatePostLoadCommandSpan(
	raw string,
	line int,
	vocab *BacktickVocab,
	postMut bool,
	postReach toolreach.Request,
) (bool, []AttributedClaimIssue, bool) {
	first := firstCommandWord(raw)
	if first == "" || !vocab.commands[first] {
		return false, nil, false
	}
	if ok, reason := checkAllCommandSegments(vocab, postReach, postMut, raw); !ok {
		return false, []AttributedClaimIssue{{
			Tool: toolreach.ProgramTool, AttributedTools: []string{toolreach.ProgramTool}, Path: raw, Span: raw, Line: line,
			Reason: reason,
		}}, true
	}
	return true, nil, true
}

func validatePostLoadBareFieldSpan(
	cat *toolvocab.Catalog,
	target, raw string,
	line int,
	auth ActorAuthority,
	idx *PrunableSchemaIndex,
	offered map[string]bool,
) (bool, []AttributedClaimIssue, bool) {
	if idx == nil || strings.Contains(raw, "(") || (strings.Contains(raw, " ") && !strings.ContainsAny(raw, ":=")) {
		return false, nil, false
	}
	p, val, hasVal, ell := splitPathAndOptionalValue(raw)
	norm := normalizeSchemaPath(p)
	if norm == "" || (cat != nil && cat.KnownName(norm) && norm != target) {
		return false, nil, false
	}
	owners := idx.AllOwners(norm)
	if !slices.Contains(owners, target) || anyOfferedOwner(owners, offered) {
		return false, nil, false
	}
	projSchema, hasSchema := resolvePostLoadSchema(target, auth)
	if !hasSchema {
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: norm, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load schema for %q is uncertified; cannot verify %q", target, raw),
		}}, true
	}
	if !toolschema.SchemaAcceptsField(projSchema, norm) && !schemaAcceptsPath(projSchema, norm) {
		rep := missingPathHead(projSchema, norm)
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: rep, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load %s projected schema lacks %q", target, rep),
		}}, true
	}
	if hasVal && !ell {
		if iss := validateValueAtPath(target, projSchema, norm, val, line); len(iss) > 0 {
			return false, iss, true
		}
	}
	return true, nil, true
}

func anyOfferedOwner(owners []string, offered map[string]bool) bool {
	for _, o := range owners {
		if offered[o] {
			return true
		}
	}
	return false
}

func resolvePostLoadSchema(tool string, auth ActorAuthority) (map[string]any, bool) {
	if auth.PostLoadSchemas == nil {
		return nil, false
	}
	s, ok := auth.PostLoadSchemas[tool]
	return s, ok && s != nil
}

func checkCommandInvocations(
	vocab *BacktickVocab,
	auth ActorAuthority,
	held map[string]bool,
	label string,
	subj ClaimSubject,
	sentence string,
	line int,
) []SubjectViolation {
	if vocab == nil || vocab.IsAuthoringReferenceSource(label) {
		return nil
	}
	curReach := currentCommandReach(auth, held)
	curMut := held[toolcontract.ReachCommandMutation]
	var out []SubjectViolation
	for _, bs := range ExtractInlineBackticks(sentence) {
		raw := strings.TrimSpace(bs.Span)
		if raw == "" || strings.Contains(raw, "(") || !strings.Contains(raw, " ") {
			continue
		}
		for _, seg := range chainSplitRe.Split(raw, -1) {
			first := firstCommandWord(seg)
			if first == "" || !vocab.commands[first] {
				continue
			}
			if ok, reason := checkSingleCommandSegment(curReach, curMut, seg); !ok {
				out = append(out, SubjectViolation{
					Label: label, Subject: subj, Claim: raw, Line: line,
					Reason: reason,
				})
			}
		}
	}
	return out
}

func currentCommandReach(auth ActorAuthority, held map[string]bool) toolreach.Request {
	if !held[toolreach.ProgramTool] {
		return toolreach.Offered(nil)
	}
	req := toolreach.Offered([]string{toolreach.ProgramTool})
	if auth.ProgramTable != nil {
		return req.WithProgramTable(auth.ProgramTable)
	}
	progReach := auth.ProgramReach
	if progReach == nil {
		progReach = auth.Reader.CanReachProgram
	}
	return req.WithPrograms(progReach)
}

func effectsAllowMutation(eff toolcontract.CommandEffects) bool {
	mut, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	return err == nil && eff.Allows(mut)
}

func firstCommandWord(s string) string {
	words := splitCommandWords(strings.TrimSpace(s))
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

func checkAllCommandSegments(vocab *BacktickVocab, reach toolreach.Request, allowMut bool, cmd string) (bool, string) {
	for _, seg := range chainSplitRe.Split(cmd, -1) {
		first := firstCommandWord(seg)
		if first == "" || (vocab != nil && !vocab.commands[first]) {
			continue
		}
		if ok, reason := checkSingleCommandSegment(reach, allowMut, seg); !ok {
			return false, reason
		}
	}
	return true, ""
}

var anglePlaceholderRe = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9_.-]*>`)

func checkSingleCommandSegment(reach toolreach.Request, allowMut bool, seg string) (bool, string) {
	if reach.Status(toolreach.ProgramTool) == toolreach.Absent {
		return false, fmt.Sprintf("command invocation %q is uncertified or command tool is not offered", seg)
	}
	cleaned := anglePlaceholderRe.ReplaceAllString(seg, "PLACEHOLDER")
	if hasUnquotedOutputRedirect(cleaned) && !allowMut {
		return false, fmt.Sprintf("output redirect in %q requires command mutation", seg)
	}
	words := normalizeProseCommandWords(splitCommandWords(strings.TrimSpace(cleaned)))
	if len(words) == 0 {
		return true, ""
	}
	if len(words) == 1 {
		if !reach.Program(words[0]) {
			return false, fmt.Sprintf("command %q is not admitted on target program surface", words[0])
		}
		return true, ""
	}
	if reach.ProgramInvocation(words...) {
		return true, ""
	}
	if len(words) == 2 && strings.HasPrefix(words[1], "-") {
		if reach.ProgramInvocation(words[0], words[1], "PATTERN") ||
			(words[0] == "sed" && words[1] == "-n" && reach.ProgramInvocation("sed", "-n", "1,2p", "FILE")) {
			return true, ""
		}
	}
	if len(words) == 3 && words[0] == "git" && words[1] == "-c" && strings.Contains(words[2], "=") {
		if reach.Program("git status") {
			return true, ""
		}
	}
	if bound, ok := commandmode.Resolve(context.Background(), words); ok {
		return false, fmt.Sprintf("command mode %s[%s] in %q is not admitted", bound.Command, bound.Mode, seg)
	}
	return false, fmt.Sprintf("command invocation %q does not match an admitted command form", seg)
}

// CoordinatorTurnSpec describes inputs for resolving production coordinator turn authority.
type CoordinatorTurnSpec struct {
	Catalog               *extpacks.EffectiveCatalog
	SessionMode           api.SessionMode
	Posture               api.SessionPosture
	SurfaceID             string
	RootCount             int
	HasGit                bool
	WebSearch             bool
	CommandExternalHidden bool
	SpawnAllowlist        []string
	Loaded                map[string]bool
}

// ResolveCoordinatorTurnAuthority computes production coordinator turn authority,
// including deferred-tool schema projections and typed program admission.
func ResolveCoordinatorTurnAuthority(ctx context.Context, spec CoordinatorTurnSpec) (ActorAuthority, error) {
	eff := spec.Catalog
	if eff == nil {
		var err error
		if eff, err = extpacks.CatalogForConsumers(); err != nil {
			return ActorAuthority{}, err
		}
	}
	mode := spec.SessionMode
	if mode == "" {
		mode = api.SessionModeAgent
	}
	posture := spec.Posture
	if posture == "" {
		posture = api.SessionPostureBuild
	}
	narrowed, schemas, reg, exec, err := buildTurnExecutor(eff, posture, false, spec.CommandExternalHidden)
	if err != nil {
		return ActorAuthority{}, err
	}
	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       exec.Metadata,
		ProjectRootCount: func(context.Context, *api.Session) int { return spec.RootCount },
	})
	host := prompts.HostFacts{
		SessionMode: string(mode), Profiles: narrowed, Schemas: schemas, RootCount: spec.RootCount,
		HasGit: spec.HasGit, HasAnyGitRoot: spec.HasGit, WebSearch: spec.WebSearch,
		SpawnAllowlist: spec.SpawnAllowlist, CommandExternalHidden: spec.CommandExternalHidden,
	}
	deps := promptloop.ContextDeps{
		Tools:              reg,
		Policy:             policy,
		LoadedTools:        func(string) map[string]bool { return maps.Clone(spec.Loaded) },
		ProjectRootCount:   func(context.Context, *api.Session) int { return spec.RootCount },
		HostFacts:          func(context.Context, *api.Session) (prompts.HostFacts, error) { return host, nil },
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return spec.WebSearch }},
	}
	sess := &api.Session{ID: "coord-check", Mode: mode, Posture: posture, AgentType: prompts.CoordinatorProfileID}
	if spec.RootCount > 0 {
		sess.WorkspacePath = "/tmp/proj"
	}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Inspect state"}}
	runCtx := api.CoordinatorRunContext{AllowedAgents: slices.Clone(spec.SpawnAllowlist)}
	if mode != api.SessionModeAsk && spec.SurfaceID != "" {
		if surface.ResolveTurnProfile(runCtx, sess, history).SurfaceID != spec.SurfaceID {
			runCtx.RunID = "coord-check-run"
			runCtx.WorkflowID = "coord-check-wf"
			runCtx.PhaseCoordinatorSurface = spec.SurfaceID
		}
	}
	var roster *inject.AgentRoster
	if spec.SpawnAllowlist != nil {
		roster = &inject.AgentRoster{Effective: slices.Clone(spec.SpawnAllowlist)}
	}
	frame := inject.CoordinatorTurnFrame{
		RunContext: runCtx,
		Roster:     roster,
		Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID},
	}
	turn, err := promptloop.ProjectTurnTools(ctx, deps, sess, history, frame)
	if err != nil {
		return ActorAuthority{}, err
	}
	metas, surfaceID := turn.Metas, turn.SurfaceID
	filter := platform.ToolFilter{ProfileID: prompts.CoordinatorProfileID, SurfaceID: surfaceID, ProjectRootCount: spec.RootCount}
	progReach, err := exec.Metadata.ProgramReach(ctx, filter)
	if err != nil {
		return ActorAuthority{}, err
	}
	progTable, err := exec.Metadata.ProgramTable(ctx, filter)
	if err != nil {
		return ActorAuthority{}, err
	}
	reader, err := prompts.NewReader(host, turn.Projection(metas))
	if err != nil {
		return ActorAuthority{}, err
	}
	return AuthorityFromReaderAndMetas(reader, progReach, metas, WithProgramTable(progTable), WithPostLoadAuthority(turn.DeferredSchemas, progTable)), nil
}

// ResolveWorkerAuthorityForSession builds the production-projected ActorAuthority
// for a worker agent under the active session mode and posture.
func ResolveWorkerAuthorityForSession(
	ctx context.Context,
	eff *extpacks.EffectiveCatalog,
	sessionMode api.SessionMode,
	posture api.SessionPosture,
	rootCount int,
	hasGit, onWorkerBranch, webSearch, externalHidden bool,
	agentID string,
) (ActorAuthority, error) {
	if eff == nil {
		var err error
		if eff, err = extpacks.CatalogForConsumers(); err != nil {
			return ActorAuthority{}, err
		}
	}
	defs, err := agentdef.LoadEffectiveWithCatalog(eff)
	if err != nil {
		return ActorAuthority{}, err
	}
	var target *agentdef.Profile
	for i := range defs {
		if defs[i].ID == agentID {
			target = &defs[i]
			break
		}
	}
	if target == nil {
		return ActorAuthority{}, fmt.Errorf("worker agent %q not found", agentID)
	}
	askChild := sessionMode == api.SessionModeAsk
	narrowed, schemas, reg, exec, err := buildTurnExecutor(eff, posture, askChild, externalHidden)
	if err != nil {
		return ActorAuthority{}, err
	}
	rc := rootCount
	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       exec.Metadata,
		ProjectRootCount: func(context.Context, *api.Session) int { return rc },
	})
	host := prompts.HostFacts{
		SessionMode: string(sessionMode), Profiles: narrowed, Schemas: schemas, RootCount: rc,
		HasGit: hasGit, HasAnyGitRoot: hasGit, OnWorkerBranch: onWorkerBranch,
		WebSearch: webSearch, CommandExternalHidden: externalHidden,
	}
	deps := promptloop.ContextDeps{
		Tools:              reg,
		Policy:             policy,
		LoadedTools:        func(string) map[string]bool { return map[string]bool{} },
		ProjectRootCount:   func(context.Context, *api.Session) int { return rc },
		HostFacts:          func(context.Context, *api.Session) (prompts.HostFacts, error) { return host, nil },
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return webSearch }},
	}
	workerSess := &api.Session{ID: "worker-check", ParentSessionID: "coord-check", Mode: sessionMode, Posture: posture, AgentType: target.ID, WorkspacePath: "/tmp/proj"}
	frame := inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: target.ToolProfile}}
	turn, err := promptloop.ProjectTurnTools(ctx, deps, workerSess, nil, frame)
	if err != nil {
		return ActorAuthority{}, err
	}
	metas := turn.Metas
	filter := platform.ToolFilter{ProfileID: target.ToolProfile, ProjectRootCount: rc}
	progReach, err := exec.Metadata.ProgramReach(ctx, filter)
	if err != nil {
		return ActorAuthority{}, err
	}
	progTable, err := exec.Metadata.ProgramTable(ctx, filter)
	if err != nil {
		return ActorAuthority{}, err
	}
	reader, err := prompts.NewReader(host, turn.Projection(metas))
	if err != nil {
		return ActorAuthority{}, err
	}
	return AuthorityFromReaderAndMetas(reader, progReach, metas, WithProgramTable(progTable), WithPostLoadAuthority(turn.DeferredSchemas, progTable)), nil
}

func buildTurnExecutor(
	eff *extpacks.EffectiveCatalog,
	posture api.SessionPosture,
	clampReadOnly, externalHidden bool,
) ([]sandbox.ToolProfile, *toolschema.Config, *tools.DefaultRegistry, *toolexecution.Executor, error) {
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	postureReg, err := sessionprofiles.LoadPostureRegistry()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var allowedEffects toolcontract.CommandEffects
	foundPosture := false
	for _, p := range postureReg.List() {
		if p.ID == posture {
			allowedEffects = p.CommandEffects
			foundPosture = true
			break
		}
	}
	if !foundPosture {
		return nil, nil, nil, nil, fmt.Errorf("unknown session posture %q", posture)
	}
	narrowed, err := narrowPostureProfiles(profiles, allowedEffects, clampReadOnly)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	reg, err := buildCatalogToolRegistry(eff)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sandboxCfg, err := sandbox.LoadConfig()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	exec := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandboxCfg, narrowed)), reg, toolprofiles.DefaultToolProfileID)
	exec.Metadata.SetToolSchemas(schemas)
	exec.Metadata.SetCommandExternalHidden(externalHidden)
	return narrowed, schemas, reg, exec, nil
}

func sortedMetaNames(metas []tools.ToolMeta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.Name
	}
	sort.Strings(out)
	return out
}

func metaSchemaMap(metas []tools.ToolMeta) map[string]map[string]any {
	out := make(map[string]map[string]any, len(metas))
	for _, m := range metas {
		out[m.Name] = m.ArgsSchema
	}
	return out
}

func splitCommandWords(seg string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(seg) {
				i++
				cur.WriteByte(seg[i])
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			inWord = true
			continue
		}
		if ch == ' ' || ch == '\t' {
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
			continue
		}
		cur.WriteByte(ch)
		inWord = true
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

func normalizeProseCommandWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "…" || w == "..." {
			continue
		}
		switch w {
		case "A,Bp":
			w = "1,2p"
		case "A,B":
			w = "1,2"
		}
		out = append(out, w)
	}
	return out
}

func hasUnquotedOutputRedirect(seg string) bool {
	var quote byte
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if quote != 0 {
			if ch == '\\' && i+1 < len(seg) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '>' && (i == 0 || (seg[i-1] != '-' && seg[i-1] != '=')) && (i+1 >= len(seg) || seg[i+1] != '&') {
			return true
		}
	}
	return false
}
