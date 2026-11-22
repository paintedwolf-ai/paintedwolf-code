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
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/evidence"
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
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
)

// SubjectKind classifies the target actor or temporal scope of a prompt claim.
type SubjectKind string

const (
	SubjectKindSelfNow               SubjectKind = "self_now"
	SubjectKindSelfRequestable       SubjectKind = "self_requestable"
	SubjectKindNamedAgent            SubjectKind = "named_agent"
	SubjectKindFollowupLeg           SubjectKind = "followup_leg"
	SubjectKindSessionNextTurn       SubjectKind = "session_next_turn"
	SubjectKindProhibition           SubjectKind = "prohibition"
	SubjectKindHistoricalObservation SubjectKind = "historical_observation"
)

// ClaimModality classifies how a claim asserts capability or historical state.
type ClaimModality string

const (
	ModalityCallableNow  ClaimModality = "callable_now"
	ModalityRequestable  ClaimModality = "requestable"
	ModalityDelegated    ClaimModality = "delegated"
	ModalityDeferredTurn ClaimModality = "deferred_turn"
	ModalityProhibited   ClaimModality = "prohibited"
	ModalityHistorical   ClaimModality = "historical"
)

// ClaimSubject identifies the actor, time, and modality a claim describes.
type ClaimSubject struct {
	Kind     SubjectKind
	Modality ClaimModality
	AgentID  string
	Origin   api.MessageOrigin
}

// SubjectSelfNow returns the default current-turn subject for the active reader.
func SubjectSelfNow() ClaimSubject {
	return ClaimSubject{Kind: SubjectKindSelfNow, Modality: ModalityCallableNow, Origin: api.MessageOriginHost}
}

// SubjectSelfRequestable returns the subject for explicit request_tools discovery clauses.
func SubjectSelfRequestable() ClaimSubject {
	return ClaimSubject{Kind: SubjectKindSelfRequestable, Modality: ModalityRequestable, Origin: api.MessageOriginHost}
}

// SubjectNamedAgent returns the delegated subject for a named worker agent profile.
func SubjectNamedAgent(agentID string) ClaimSubject {
	return ClaimSubject{Kind: SubjectKindNamedAgent, Modality: ModalityDelegated, AgentID: strings.TrimSpace(agentID), Origin: api.MessageOriginHost}
}

// SubjectFollowupLeg returns the subject for worker-closeout follow-up leg recommendations.
func SubjectFollowupLeg() ClaimSubject {
	return ClaimSubject{Kind: SubjectKindFollowupLeg, Modality: ModalityDeferredTurn, Origin: api.MessageOriginHost}
}

// SubjectSessionNextTurn returns the subject for coordinator-turn-closeout next-turn advice.
func SubjectSessionNextTurn() ClaimSubject {
	return ClaimSubject{Kind: SubjectKindSessionNextTurn, Modality: ModalityDeferredTurn, Origin: api.MessageOriginHost}
}

// SubjectProhibition returns the subject for explicit negative prohibitions.
func SubjectProhibition() ClaimSubject {
	return ClaimSubject{Kind: SubjectKindProhibition, Modality: ModalityProhibited, Origin: api.MessageOriginHost}
}

// SubjectHistoricalObservation returns the subject for historical observations or non-host quotes.
func SubjectHistoricalObservation(origin api.MessageOrigin) ClaimSubject {
	return ClaimSubject{Kind: SubjectKindHistoricalObservation, Modality: ModalityHistorical, Origin: origin}
}

// ActorAuthority captures a resolved actor's prompt Reader, program reach, and projected schemas.
type ActorAuthority struct {
	Reader       prompts.Reader
	ProgramReach toolreach.Programs
	Metas        []tools.ToolMeta
	Schemas      map[string]map[string]any
}

// AuthorityFromReaderAndMetas constructs an ActorAuthority from a Reader, ProgramReach, and ToolMetas.
func AuthorityFromReaderAndMetas(reader prompts.Reader, progReach toolreach.Programs, metas []tools.ToolMeta) ActorAuthority {
	schemas := make(map[string]map[string]any, len(metas))
	for _, m := range metas {
		schemas[m.Name] = m.ArgsSchema
	}
	if progReach == nil {
		progReach = reader.CanReachProgram
	}
	return ActorAuthority{Reader: reader, ProgramReach: progReach, Metas: slices.Clone(metas), Schemas: schemas}
}

// ClaimEvaluationContext bundles catalogs and actor authorities for claim checking.
type ClaimEvaluationContext struct {
	Catalog           *toolvocab.Catalog
	Prunable          *PrunableSchemaIndex
	Vocab             *BacktickVocab
	Self              ActorAuthority
	SessionMode       api.SessionMode
	Posture           api.SessionPosture
	FollowupLeg       *ActorAuthority
	SessionNextTurn   *ActorAuthority
	ResolveNamedAgent func(agentID string) (ActorAuthority, error)
}

// ClaimBlock pairs a host-authored prose block or clause with its ClaimSubject.
type ClaimBlock struct {
	Label   string
	Subject ClaimSubject
	Text    string
}

// SubjectViolation reports one capability, schema, or receipt violation on a ClaimSubject.
type SubjectViolation struct {
	Label   string
	Subject ClaimSubject
	Claim   string
	Reason  string
	Line    int
}

var (
	prohibitionClauseRe = regexp.MustCompile(`(?i)(?:^\s*(?:do\s+not|never)\b|\b(?:do\s+not|never)\s+(?:call|run|use|pass|set|assign|mint|combine|invoke)\b|\bis\s+not\s+(?:available|offered|permitted|allowed)\b|\bare\s+(?:not\s+available|not\s+offered|refused|forbidden)\b|\bwithout\s+` + "`?[a-z_]+" + `|\bcannot\s+(?:call|run|use|allow)\b)`)
	writePromiseProseRe = regexp.MustCompile(`(?i)\b(?:product\s+edits\s+only|scope\.mode\s*[:=]?\s*write|staged\s+command\s+redirect|apply\s+the\s+change\s+in\s+chunks|redo\s+the\s+change\s+in\s+chunks|create\s+or\s+edit\s+files|writes?\s+project\s+files)\b`)
	snakeWordRe         = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	bareToolWordRe      = regexp.MustCompile("(?i)`([a-z][a-z0-9_-]*)`")
	chainSplitRe        = regexp.MustCompile(`\s*(?:&&|\|\||[|;])\s*`)
	gitMutSubcommands   = map[string]bool{
		"add": true, "commit": true, "restore": true, "reset": true, "checkout": true,
		"merge": true, "rebase": true, "cherry-pick": true, "tag": true, "stash": true,
		"apply": true, "am": true, "rm": true, "mv": true,
	}
	gitExtSubcommands = map[string]bool{
		"bisect": true, "worktree": true, "push": true, "pull": true, "fetch": true, "clone": true, "submodule": true,
	}
)

// EvaluateClaimBlock evaluates a ClaimBlock against the authority of its ClaimSubject.
func EvaluateClaimBlock(evalCtx ClaimEvaluationContext, block ClaimBlock) []SubjectViolation {
	cat := evalCtx.Catalog
	if cat == nil {
		cat = toolvocab.StockCatalog()
	}
	vocab := evalCtx.Vocab
	if vocab == nil {
		vocab, _ = StockBacktickVocab()
	}
	switch block.Subject.Kind {
	case SubjectKindProhibition:
		return nil
	case SubjectKindHistoricalObservation:
		return evaluateHistoricalBlock(vocab, block)
	case SubjectKindSelfRequestable:
		return evaluateRequestableClause(cat, vocab, evalCtx.Self, block.Label, block.Subject, block.Text, 1)
	case SubjectKindNamedAgent:
		return evaluateNamedAgentBlock(cat, evalCtx, vocab, block)
	case SubjectKindFollowupLeg:
		if evalCtx.FollowupLeg == nil {
			return []SubjectViolation{{Label: block.Label, Subject: block.Subject, Reason: "missing FollowupLeg authority", Line: 1}}
		}
		return evaluateDeferredTurnBlock(cat, evalCtx.Prunable, vocab, *evalCtx.FollowupLeg, block)
	case SubjectKindSessionNextTurn:
		if evalCtx.SessionNextTurn == nil {
			return []SubjectViolation{{Label: block.Label, Subject: block.Subject, Reason: "missing SessionNextTurn authority", Line: 1}}
		}
		return evaluateDeferredTurnBlock(cat, evalCtx.Prunable, vocab, *evalCtx.SessionNextTurn, block)
	case SubjectKindSelfNow:
		return evaluateActorProse(cat, evalCtx.Prunable, vocab, evalCtx.Self, block.Label, block.Subject, block.Text, false)
	default:
		return evaluateActorProse(cat, evalCtx.Prunable, vocab, evalCtx.Self, block.Label, block.Subject, block.Text, false)
	}
}

func evaluateHistoricalBlock(vocab *BacktickVocab, block ClaimBlock) []SubjectViolation {
	if block.Subject.Origin != "" && block.Subject.Origin != api.MessageOriginHost {
		return nil
	}
	var out []SubjectViolation
	knownKinds := evidence.ActiveBinding().KindShapeMap()
	seenKinds := map[string]bool{}
	for _, bs := range ExtractInlineBackticks(block.Text) {
		if m := handleSpanRe.FindStringSubmatch(bs.Span); len(m) == 2 {
			kind := m[1]
			if _, ok := knownKinds[kind]; !ok && kind != "kind" && !seenKinds[kind] {
				seenKinds[kind] = true
				out = append(out, SubjectViolation{
					Label: block.Label, Subject: block.Subject, Claim: kind + "#", Line: bs.Line,
					Reason: fmt.Sprintf("historical observation cites undeclared receipt kind %q", kind),
				})
			}
		}
	}
	for _, m := range regexp.MustCompile(`\[([a-z][a-z0-9_]*)#[0-9Nn]+\]`).FindAllStringSubmatch(block.Text, -1) {
		kind := m[1]
		if _, ok := knownKinds[kind]; !ok && kind != "kind" && !seenKinds[kind] {
			seenKinds[kind] = true
			out = append(out, SubjectViolation{
				Label: block.Label, Subject: block.Subject, Claim: kind + "#", Line: 1,
				Reason: fmt.Sprintf("historical observation cites undeclared receipt kind %q", kind),
			})
		}
	}
	if vocab != nil {
		for _, bs := range ExtractInlineBackticks(block.Text) {
			head, field, hasDot := strings.Cut(normalizeSchemaPath(bs.Span), ".")
			if !hasDot {
				continue
			}
			if (head == "receipt" || head == "proof_json" || head == "report_json") && !vocab.catalogIDs[field] && !vocab.schemaItems[field] {
				out = append(out, SubjectViolation{
					Label: block.Label, Subject: block.Subject, Claim: bs.Span, Line: bs.Line,
					Reason: fmt.Sprintf("historical observation cites undeclared receipt field %q", bs.Span),
				})
			}
		}
	}
	return out
}

func evaluateRequestableClause(
	cat *toolvocab.Catalog,
	vocab *BacktickVocab,
	self ActorAuthority,
	label string,
	subj ClaimSubject,
	text string,
	baseLine int,
) []SubjectViolation {
	offeredSet := stringSliceSet(self.Reader.Offered())
	if !offeredSet[toolreach.LoaderTool] {
		return []SubjectViolation{{
			Label: label, Subject: subj, Claim: toolreach.LoaderTool, Line: baseLine,
			Reason: "request_tools discovery clause rendered when request_tools is not offered",
		}}
	}
	reqSet := stringSliceSet(self.Reader.Requestable())
	postHeld := buildPostLoadHeld(self.Reader)
	var out []SubjectViolation
	calls := ParseCallExamples(cat, text)
	sawLoader := false
	for _, ex := range calls {
		if ex.Tool != toolreach.LoaderTool {
			continue
		}
		sawLoader = true
		needRaw, hasNeed := ex.Args["need"]
		if !hasNeed {
			out = append(out, SubjectViolation{Label: label, Subject: subj, Claim: ex.Raw, Line: baseLine, Reason: "request_tools call missing required need argument"})
			continue
		}
		needStr, ok := concreteStringOrIdent(needRaw)
		if !ok || isPlaceholderText(needStr) {
			continue
		}
		if strings.Contains(needStr, " ") {
			if len(reqSet) == 0 {
				out = append(out, SubjectViolation{Label: label, Subject: subj, Claim: ex.Raw, Line: baseLine, Reason: "request_tools discovery clause rendered with zero requestable tools"})
			}
			continue
		}
		admittedViaCommand := reqSet[toolreach.ProgramTool] && toolcontract.CommandLanguageReachable(needStr, postHeld) && !self.Reader.CanReachProgram(needStr)
		if !reqSet[needStr] && !admittedViaCommand {
			out = append(out, SubjectViolation{
				Label: label, Subject: subj, Claim: ex.Raw, Line: baseLine,
				Reason: fmt.Sprintf("request_tools target %q is neither in Requestable() nor newly admitted via loadable command", needStr),
			})
		}
	}
	if !sawLoader && len(reqSet) == 0 {
		out = append(out, SubjectViolation{Label: label, Subject: subj, Claim: toolreach.LoaderTool, Line: baseLine, Reason: "requestable clause rendered with empty Requestable() set"})
	}
	postAuth := ActorAuthority{
		Reader:       self.Reader,
		ProgramReach: func(prog string) bool { return toolcontract.CommandLanguageReachable(prog, postHeld) },
		Metas:        self.Metas,
		Schemas:      self.Schemas,
	}
	for _, name := range toolvocab.UnreachableNames(cat, text, postHeld) {
		out = append(out, SubjectViolation{Label: label, Subject: subj, Claim: name, Line: baseLine, Reason: fmt.Sprintf("post-load reach still lacks %q", name)})
	}
	for _, cmdViol := range checkCommandInvocations(vocab, postAuth, postHeld, label, subj, text, baseLine) {
		out = append(out, cmdViol)
	}
	return out
}

func evaluateNamedAgentBlock(cat *toolvocab.Catalog, evalCtx ClaimEvaluationContext, vocab *BacktickVocab, block ClaimBlock) []SubjectViolation {
	agentID := block.Subject.AgentID
	if evalCtx.SessionMode == api.SessionModeAsk {
		if mut, ok := prompts.AgentMutationCapable(agentID); ok && mut {
			return []SubjectViolation{{
				Label: block.Label, Subject: block.Subject, Claim: agentID, Line: 1,
				Reason: fmt.Sprintf("Ask mode cannot offer mutating worker agent %q", agentID),
			}}
		}
	}
	var targetAuth ActorAuthority
	var err error
	if evalCtx.ResolveNamedAgent != nil {
		targetAuth, err = evalCtx.ResolveNamedAgent(agentID)
	} else {
		posture := evalCtx.Posture
		if posture == "" {
			posture = api.SessionPostureBuild
		}
		mode := evalCtx.SessionMode
		if mode == "" {
			mode = api.SessionModeAgent
		}
		targetAuth, err = ResolveWorkerAuthorityForSession(context.Background(), nil, mode, posture, 1, true, true, false, agentID)
	}
	if err != nil {
		return []SubjectViolation{{Label: block.Label, Subject: block.Subject, Claim: agentID, Line: 1, Reason: err.Error()}}
	}
	var out []SubjectViolation
	if !readerAllowsMutation(targetAuth.Reader) && writePromiseProseRe.MatchString(stripNegatedSegments(block.Text)) {
		out = append(out, SubjectViolation{
			Label: block.Label, Subject: block.Subject, Claim: "write_capability", Line: 1,
			Reason: fmt.Sprintf("spawn description for %q promises write capability, which its resolved Reader lacks under mode=%q posture=%q", agentID, evalCtx.SessionMode, evalCtx.Posture),
		})
	}
	held := targetAuth.Reader.Held()
	for _, word := range snakeWordRe.FindAllString(stripNegatedSegments(block.Text), -1) {
		if cat.KnownName(word) && !held[word] && !slices.Contains(targetAuth.Reader.Requestable(), word) {
			out = append(out, SubjectViolation{
				Label: block.Label, Subject: block.Subject, Claim: word, Line: 1,
				Reason: fmt.Sprintf("spawn description for %q names unoffered tool %q", agentID, word),
			})
		}
	}
	out = append(out, evaluateActorProse(cat, evalCtx.Prunable, vocab, targetAuth, block.Label, block.Subject, block.Text, true)...)
	return out
}

func evaluateDeferredTurnBlock(
	cat *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	targetAuth ActorAuthority,
	block ClaimBlock,
) []SubjectViolation {
	var out []SubjectViolation
	if !readerAllowsMutation(targetAuth.Reader) && writePromiseProseRe.MatchString(stripNegatedSegments(block.Text)) {
		out = append(out, SubjectViolation{
			Label: block.Label, Subject: block.Subject, Claim: "command_mutation", Line: 1,
			Reason: fmt.Sprintf("%s promises mutating file/redirect work, which target Reader lacks", block.Subject.Kind),
		})
	}
	out = append(out, evaluateActorProse(cat, idx, vocab, targetAuth, block.Label, block.Subject, block.Text, false)...)
	return out
}

func evaluateActorProse(
	cat *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	auth ActorAuthority,
	label string,
	subj ClaimSubject,
	text string,
	includeRequestable bool,
) []SubjectViolation {
	held := auth.Reader.Held()
	if includeRequestable {
		for _, req := range auth.Reader.Requestable() {
			held[req] = true
		}
	}
	offered := maps.Clone(held)
	schemaFn := func(tool string) (map[string]any, bool) {
		if auth.Schemas != nil {
			s, ok := auth.Schemas[tool]
			return s, ok
		}
		if idx != nil {
			return idx.BaseSchema(tool)
		}
		return nil, false
	}

	var out []SubjectViolation
	for _, sent := range splitActiveSentences(text) {
		if isProhibitionClause(sent.sentence) {
			continue
		}
		if strings.Contains(sent.sentence, "request_tools(") && subj.Kind == SubjectKindSelfNow {
			out = append(out, evaluateRequestableClause(cat, vocab, auth, label, SubjectSelfRequestable(), sent.sentence, sent.line)...)
			continue
		}
		for _, name := range toolvocab.UnreachableNames(cat, sent.sentence, held) {
			out = append(out, SubjectViolation{
				Label: label, Subject: subj, Claim: name, Line: sent.line,
				Reason: fmt.Sprintf("names unreachable tool, operation, or handle %q", name),
			})
		}
		out = append(out, checkCommandInvocations(vocab, auth, held, label, subj, sent.sentence, sent.line)...)
		if auth.Schemas != nil || idx != nil {
			for _, iss := range ValidateAttributedClaims(cat, idx, sent.sentence, offered, schemaFn) {
				claim := iss.Path
				if claim == "" {
					claim = iss.Span
				}
				out = append(out, SubjectViolation{
					Label: label, Subject: subj, Claim: claim, Line: sent.line, Reason: iss.Reason,
				})
			}
		}
	}
	return deduplicateSubjectViolations(out)
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
	if vocab == nil {
		return nil
	}
	progReach := auth.ProgramReach
	if progReach == nil {
		progReach = auth.Reader.CanReachProgram
	}
	var out []SubjectViolation
	for _, bs := range ExtractInlineBackticks(sentence) {
		raw := strings.TrimSpace(bs.Span)
		if raw == "" || strings.Contains(raw, "(") || !strings.Contains(raw, " ") {
			continue
		}
		for _, seg := range chainSplitRe.Split(raw, -1) {
			words := strings.Fields(strings.TrimSpace(seg))
			if len(words) < 2 {
				continue
			}
			prog := words[0]
			if !vocab.commands[prog] {
				continue
			}
			if !held[toolreach.ProgramTool] || !progReach(prog) {
				out = append(out, SubjectViolation{
					Label: label, Subject: subj, Claim: raw, Line: line,
					Reason: fmt.Sprintf("command invocation %q requires reachable program %q", raw, prog),
				})
				continue
			}
			if prog == "git" {
				sub := words[1]
				if gitExtSubcommands[sub] && !held[toolcontract.ReachCommandExternalExec] {
					out = append(out, SubjectViolation{Label: label, Subject: subj, Claim: raw, Line: line, Reason: fmt.Sprintf("git %s requires external command execution", sub)})
				}
				if (gitMutSubcommands[sub] || sub == "bisect" || sub == "worktree") && !held[toolcontract.ReachCommandMutation] {
					out = append(out, SubjectViolation{Label: label, Subject: subj, Claim: raw, Line: line, Reason: fmt.Sprintf("git %s requires command mutation", sub)})
				}
			}
		}
	}
	return out
}

// ResolveWorkerAuthorityForSession builds the production-projected ActorAuthority
// for a worker agent under the active session mode and posture.
func ResolveWorkerAuthorityForSession(
	ctx context.Context,
	eff *extpacks.EffectiveCatalog,
	sessionMode api.SessionMode,
	posture api.SessionPosture,
	rootCount int,
	hasGit, webSearch, externalHidden bool,
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
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return ActorAuthority{}, err
	}
	postureReg, err := sessionprofiles.LoadPostureRegistry()
	if err != nil {
		return ActorAuthority{}, err
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
		return ActorAuthority{}, fmt.Errorf("unknown session posture %q", posture)
	}
	askChild := sessionMode == api.SessionModeAsk
	narrowed, err := narrowPostureProfiles(profiles, allowedEffects, askChild)
	if err != nil {
		return ActorAuthority{}, err
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return ActorAuthority{}, err
	}
	reg, err := buildCatalogToolRegistry(eff)
	if err != nil {
		return ActorAuthority{}, err
	}
	sandboxCfg, err := sandbox.LoadConfig()
	if err != nil {
		return ActorAuthority{}, err
	}
	exec := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandboxCfg, narrowed)), reg, toolprofiles.DefaultToolProfileID)
	exec.Metadata.SetToolSchemas(schemas)
	exec.Metadata.SetCommandExternalHidden(externalHidden)
	rc := max(rootCount, 1)
	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       exec.Metadata,
		ProjectRootCount: func(context.Context, *api.Session) int { return rc },
	})
	deps := promptloop.ContextDeps{
		Tools:              reg,
		Policy:             policy,
		LoadedTools:        func(string) map[string]bool { return map[string]bool{} },
		ProjectRootCount:   func(context.Context, *api.Session) int { return rc },
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return webSearch }},
	}
	workerSess := &api.Session{ID: "worker-check", ParentSessionID: "coord-check", Mode: sessionMode, Posture: posture, AgentType: target.ID, WorkspacePath: "/tmp/proj"}
	metas, plan, _, err := promptloop.ProjectTurnTools(ctx, deps, workerSess, nil, inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: target.ToolProfile}})
	if err != nil {
		return ActorAuthority{}, err
	}
	progReach, err := exec.Metadata.ProgramReach(ctx, platform.ToolFilter{ProfileID: target.ToolProfile, ProjectRootCount: rc})
	if err != nil {
		return ActorAuthority{}, err
	}
	offered := make([]string, len(metas))
	for i, m := range metas {
		offered[i] = m.Name
	}
	sort.Strings(offered)
	mutCapable, _ := prompts.AgentMutationCapable(target.ID)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: target.ToolProfile, SessionMode: string(sessionMode), Profiles: narrowed,
		Plan: plan, Visible: offered, Schemas: schemas, RootCount: rc,
		HasGit: hasGit, HasAnyGitRoot: hasGit, OnWorkerBranch: hasGit && mutCapable && !askChild,
		WebSearch: webSearch, CommandExternalHidden: externalHidden,
	})
	if err != nil {
		return ActorAuthority{}, err
	}
	return AuthorityFromReaderAndMetas(reader, progReach, metas), nil
}

func buildCatalogToolRegistry(eff *extpacks.EffectiveCatalog) (*tools.DefaultRegistry, error) {
	regSchemas, err := extpacks.LoadEffectiveRegistrySchemas(eff)
	if err != nil {
		return nil, err
	}
	reg, err := tools.NewCatalogRegistry(regSchemas)
	if err != nil {
		return nil, err
	}
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	for name := range regSchemas.Tools {
		if toolcontract.IsCommandOperation(name) {
			_ = reg.RegisterOperation(name)
			continue
		}
		if _, ok := toolcontract.Lookup(name); ok {
			if regErr := reg.Register(name, noop); regErr != nil {
				return nil, regErr
			}
		}
	}
	return reg, nil
}

func narrowPostureProfiles(profiles []sandbox.ToolProfile, allowed toolcontract.CommandEffects, clampReadOnly bool) ([]sandbox.ToolProfile, error) {
	if clampReadOnly {
		ro, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
		if err != nil {
			return nil, err
		}
		allowed &= ro
	}
	out := slices.Clone(profiles)
	for i := range out {
		out[i].CommandEffects &= allowed
	}
	return out, nil
}

type sentenceContext struct {
	sentence      string
	line          int
	sentenceTools []string
	sectionTools  []string
}

func splitActiveSentences(text string) []sentenceContext {
	cat := toolvocab.StockCatalog()
	inFence := ""
	var sectionTools []string
	var out []sentenceContext
	for idx, rawLine := range strings.Split(text, "\n") {
		lineNum := idx + 1
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if inFence == "" {
				inFence = marker
			} else if inFence == marker {
				inFence = ""
			}
			continue
		}
		if inFence != "" || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			sectionTools = extractKnownToolMentions(cat, trimmed)
		}
		lineTools := extractKnownToolMentions(cat, rawLine)
		for _, t := range lineTools {
			if !slices.Contains(sectionTools, t) {
				sectionTools = append(sectionTools, t)
			}
		}
		sort.Strings(sectionTools)
		for _, clause := range splitSentenceClauses(rawLine) {
			cTools := extractKnownToolMentions(cat, clause)
			out = append(out, sentenceContext{
				sentence:      clause,
				line:          lineNum,
				sentenceTools: cTools,
				sectionTools:  slices.Clone(sectionTools),
			})
		}
	}
	return out
}

func splitSentenceClauses(line string) []string {
	var out []string
	depthParen, depthBracket, depthBrace := 0, 0, 0
	inTicks := false
	var quote byte
	start := 0
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '`' && quote == 0 {
			inTicks = !inTicks
			continue
		}
		if inTicks {
			continue
		}
		if quote != 0 {
			if r == '\\' && i+1 < len(runes) {
				i++
				continue
			}
			if r < 128 && byte(r) == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = byte(r)
		case '(':
			depthParen++
		case ')':
			if depthParen > 0 {
				depthParen--
			}
		case '[':
			depthBracket++
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
		case '{':
			depthBrace++
		case '}':
			if depthBrace > 0 {
				depthBrace--
			}
		case '.', ';', '—':
			if depthParen == 0 && depthBracket == 0 && depthBrace == 0 {
				if r == '.' && i+1 < len(runes) && runes[i+1] != ' ' {
					continue
				}
				if seg := strings.TrimSpace(string(runes[start : i+1])); seg != "" {
					out = append(out, seg)
				}
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		out = append(out, tail)
	}
	return out
}

func extractKnownToolMentions(c *toolvocab.Catalog, s string) []string {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, t := range toolvocab.ToolsIn(c, s) {
		if c.KnownName(t) {
			seen[t] = true
		}
	}
	for _, m := range bareToolWordRe.FindAllStringSubmatch(s, -1) {
		if c.KnownName(m[1]) {
			seen[m[1]] = true
		}
	}
	return sortedTrueKeys(seen)
}

func isProhibitionClause(clause string) bool {
	return prohibitionClauseRe.MatchString(clause)
}

func stripNegatedSegments(text string) string {
	var kept []string
	for _, sc := range splitActiveSentences(text) {
		if !isProhibitionClause(sc.sentence) {
			kept = append(kept, sc.sentence)
		}
	}
	return strings.Join(kept, " ")
}

func readerAllowsMutation(r prompts.Reader) bool {
	if vars, err := prompts.Vars(r, map[string]any{}); err == nil {
		hasWrite, _ := vars["profile_has_write_tools"].(bool)
		mutCap, _ := vars["profile_mutation_capable"].(bool)
		if hasWrite || mutCap {
			return true
		}
	}
	return r.Held()[toolcontract.ReachCommandMutation]
}

func buildPostLoadHeld(r prompts.Reader) map[string]bool {
	toolSet := map[string]bool{}
	var visible []string
	for _, name := range append(slices.Clone(r.Offered()), r.Requestable()...) {
		if !toolSet[name] {
			toolSet[name] = true
			visible = append(visible, name)
		}
	}
	sort.Strings(visible)
	hidden := false
	if vars, err := prompts.Vars(r, map[string]any{}); err == nil {
		hidden, _ = vars["command_external_hidden"].(bool)
	}
	const pid = "post_load"
	postReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: pid,
		Profiles: []sandbox.ToolProfile{{
			ID:             pid,
			Tools:          toolSet,
			CommandEffects: r.Effects(),
		}},
		Visible:               visible,
		Loaded:                toolSet,
		RootCount:             1,
		HasGit:                true,
		WebSearch:             true,
		CommandExternalHidden: hidden,
	})
	if err != nil {
		return r.Held()
	}
	return postReader.Held()
}

func deduplicateSubjectViolations(in []SubjectViolation) []SubjectViolation {
	type key struct {
		kind  SubjectKind
		agent string
		claim string
		line  int
	}
	seen := map[key]bool{}
	var out []SubjectViolation
	for _, v := range in {
		k := key{v.Subject.Kind, v.Subject.AgentID, v.Claim, v.Line}
		if !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}
