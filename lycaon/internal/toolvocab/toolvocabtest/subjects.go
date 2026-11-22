package toolvocabtest

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	writePromiseProseRe = regexp.MustCompile(`(?i)\b(?:product\s+edits\s+only|scope\.mode\s*[:=]?\s*write|staged\s+command\s+redirect|apply\s+the\s+change\s+in\s+chunks|redo\s+the\s+change\s+in\s+chunks|create\s+or\s+edit\s+files|writes?\s+project\s+files)\b`)
	snakeWordRe         = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	chainSplitRe        = regexp.MustCompile(`\s*(?:&&|\|\||[|;])\s*`)
	preLoadTargetRe     = regexp.MustCompile("(?i)\\b(?:load|request|unlock|enable)\\s+(?:tool\\s+)?(?:\\*\\*)?`([a-z][a-z0-9_]*)`(?:\\*\\*)?\\s+(?:with|via|through|using|from|by\\s+calling)\\s+(?:\\*\\*)?`?$")
	preLoadVerbRe       = regexp.MustCompile(`(?i)\b(?:load|request|unlock|enable)\s+(?:tool\s+)?$`)
	preLoadPrepRe       = regexp.MustCompile(`(?i)^\s+(?:via|with|through|using|from|by\s+calling)\s+$`)
	afterLoaderPrepRe   = regexp.MustCompile(`(?i)^\s+(?:after|once\s+loaded\s+via|following)\s+(?:calling\s+)?$`)
	postLoadSeqCueRe    = regexp.MustCompile(`(?i)(?:\bbefore\b|\bthen\b|\bfirst\b|\bafter\b|\bonce\b|\bto\s+[a-z]+)`)
	unseqConjRe         = regexp.MustCompile(`(?i),\s*and\s+(?:run|use|call|invoke|execute|apply)\b`)
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
		return evaluateRequestableBlock(cat, evalCtx.Prunable, vocab, evalCtx.Self, block)
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

type loadBinding struct {
	callStart int
	callEnd   int
	target    string
}

func evaluateRequestableBlock(
	cat *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	self ActorAuthority,
	block ClaimBlock,
) []SubjectViolation {
	offeredSet := stringSliceSet(self.Reader.Offered())
	if !offeredSet[toolreach.LoaderTool] {
		return []SubjectViolation{{
			Label: block.Label, Subject: block.Subject, Claim: toolreach.LoaderTool, Line: 1,
			Reason: "request_tools discovery clause rendered when request_tools is not offered",
		}}
	}
	if len(self.Reader.Requestable()) == 0 {
		return []SubjectViolation{{
			Label: block.Label, Subject: block.Subject, Claim: toolreach.LoaderTool, Line: 1,
			Reason: "requestable clause rendered with empty Requestable() set",
		}}
	}
	return evaluateActorProse(cat, idx, vocab, self, block.Label, block.Subject, block.Text, false)
}

func extractSentenceLoadBindings(
	cat *toolvocab.Catalog,
	auth ActorAuthority,
	label string,
	subj ClaimSubject,
	sentence string,
	line int,
) ([]loadBinding, []SubjectViolation) {
	if subj.Kind != SubjectKindSelfNow && subj.Kind != SubjectKindSelfRequestable {
		return nil, nil
	}
	offeredSet := stringSliceSet(auth.Reader.Offered())
	reqSet := stringSliceSet(auth.Reader.Requestable())
	postHeld := buildPostLoadHeld(auth.Reader)
	var bindings []loadBinding
	var out []SubjectViolation
	reqSubj := SubjectSelfRequestable()
	for _, ex := range ParseCallExamples(cat, sentence) {
		if ex.Tool != toolreach.LoaderTool || isClaimSpanNegated(sentence, ex.Start, ex.End) {
			continue
		}
		if !offeredSet[toolreach.LoaderTool] {
			out = append(out, SubjectViolation{
				Label: label, Subject: reqSubj, Claim: toolreach.LoaderTool, Line: line,
				Reason: "request_tools discovery clause rendered when request_tools is not offered",
			})
			continue
		}
		needRaw, hasNeed := ex.Args["need"]
		if !hasNeed {
			if !ex.HasEllipsis {
				out = append(out, SubjectViolation{Label: label, Subject: reqSubj, Claim: ex.Raw, Line: line, Reason: "request_tools call missing required need argument"})
			}
			continue
		}
		needStr, ok := concreteStringOrIdent(needRaw)
		if !ok || isPlaceholderText(needStr) {
			continue
		}
		if strings.Contains(needStr, " ") {
			if len(reqSet) == 0 {
				out = append(out, SubjectViolation{Label: label, Subject: reqSubj, Claim: ex.Raw, Line: line, Reason: "request_tools discovery clause rendered with zero requestable tools"})
				continue
			}
			if m := preLoadTargetRe.FindStringSubmatch(sentence[:ex.Start]); len(m) == 2 {
				if !reqSet[m[1]] {
					out = append(out, SubjectViolation{Label: label, Subject: reqSubj, Claim: ex.Raw, Line: line, Reason: fmt.Sprintf("request_tools target %q is not in Requestable()", m[1])})
				} else {
					bindings = append(bindings, loadBinding{callStart: ex.Start, callEnd: ex.End, target: m[1]})
				}
			}
			continue
		}
		admittedViaCommand := reqSet[toolreach.ProgramTool] && toolcontract.CommandLanguageReachable(needStr, postHeld) && !auth.Reader.CanReachProgram(needStr)
		if !reqSet[needStr] && !admittedViaCommand {
			out = append(out, SubjectViolation{
				Label: label, Subject: reqSubj, Claim: ex.Raw, Line: line,
				Reason: fmt.Sprintf("request_tools target %q is neither in Requestable() nor newly admitted via loadable command", needStr),
			})
			continue
		}
		bindings = append(bindings, loadBinding{callStart: ex.Start, callEnd: ex.End, target: needStr})
	}
	return bindings, out
}

func isSpanSequencedWithLoader(sentence string, spStart, spEnd int, b loadBinding) bool {
	s, e := expandFormattingBounds(sentence, spStart, spEnd)
	bStart, bEnd := expandFormattingBounds(sentence, b.callStart, b.callEnd)
	if e <= bStart {
		between := sentence[e:bStart]
		if preLoadVerbRe.MatchString(sentence[:s]) && preLoadPrepRe.MatchString(between) {
			return true
		}
		return afterLoaderPrepRe.MatchString(between)
	}
	if s >= bEnd {
		between := sentence[bEnd:s]
		if unseqConjRe.MatchString(between) && !strings.Contains(strings.ToLower(between), "first") && !strings.Contains(strings.ToLower(between), "then") {
			return false
		}
		return postLoadSeqCueRe.MatchString(between)
	}
	return false
}

func isBacktickAuthorizedByLoad(
	cat *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	postHeld map[string]bool,
	sentence string,
	bs BacktickSpan,
	bindings []loadBinding,
) (authorized bool, schemaViolations []AttributedClaimIssue) {
	raw := strings.TrimSpace(bs.Span)
	for _, b := range bindings {
		if !isSpanSequencedWithLoader(sentence, bs.Start, bs.End, b) {
			continue
		}
		if raw == b.target || strings.HasPrefix(raw, b.target+".") {
			return true, nil
		}
		if strings.HasPrefix(raw, b.target+"(") {
			for _, ex := range ParseCallExamples(cat, raw) {
				if ex.Tool == b.target && idx != nil {
					if base, ok := idx.BaseSchema(b.target); ok {
						if iss := validateCallAgainstSchema(ex, base); len(iss) > 0 {
							return false, iss
						}
					}
				}
			}
			return true, nil
		}
		if b.target == toolreach.ProgramTool && vocab != nil {
			words := strings.Fields(raw)
			if len(words) >= 1 && vocab.commands[words[0]] && toolcontract.CommandLanguageReachable(words[0], postHeld) {
				if words[0] == "git" && len(words) >= 2 {
					if gitExtSubcommands[words[1]] && !postHeld[toolcontract.ReachCommandExternalExec] {
						continue
					}
					if (gitMutSubcommands[words[1]] || words[1] == "bisect" || words[1] == "worktree") && !postHeld[toolcontract.ReachCommandMutation] {
						continue
					}
				}
				return true, nil
			}
			norm := normalizeSchemaPath(raw)
			if idx != nil && slices.Contains(idx.AllOwners(norm), toolreach.ProgramTool) && postHeld[toolcontract.ReachCommandExternalExec] {
				return true, nil
			}
		}
	}
	return false, nil
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
	if !readerAllowsMutation(targetAuth.Reader) && hasNonNegatedMatch(block.Text, writePromiseProseRe) {
		out = append(out, SubjectViolation{
			Label: block.Label, Subject: block.Subject, Claim: "write_capability", Line: 1,
			Reason: fmt.Sprintf("spawn description for %q promises write capability, which its resolved Reader lacks under mode=%q posture=%q", agentID, evalCtx.SessionMode, evalCtx.Posture),
		})
	}
	held := targetAuth.Reader.Held()
	for _, sent := range splitActiveSentences(block.Text) {
		for _, loc := range snakeWordRe.FindAllStringIndex(sent.sentence, -1) {
			if isClaimSpanNegated(sent.sentence, loc[0], loc[1]) {
				continue
			}
			word := sent.sentence[loc[0]:loc[1]]
			if cat.KnownName(word) && !held[word] && !slices.Contains(targetAuth.Reader.Requestable(), word) {
				out = append(out, SubjectViolation{
					Label: block.Label, Subject: block.Subject, Claim: word, Line: sent.line,
					Reason: fmt.Sprintf("spawn description for %q names unoffered tool %q", agentID, word),
				})
			}
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
	if !readerAllowsMutation(targetAuth.Reader) && hasNonNegatedMatch(block.Text, writePromiseProseRe) {
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
	postHeld := buildPostLoadHeld(auth.Reader)

	var out []SubjectViolation
	for _, sent := range splitActiveSentences(text) {
		bindings, loadViol := extractSentenceLoadBindings(cat, auth, label, subj, sent.sentence, sent.line)
		out = append(out, loadViol...)

		var maskedSpans [][2]int
		for _, bs := range ExtractInlineBackticks(sent.sentence) {
			if isClaimSpanNegated(sent.sentence, bs.Start, bs.End) {
				maskedSpans = append(maskedSpans, [2]int{bs.Start, bs.End})
				continue
			}
			if len(bindings) > 0 {
				authOK, schemaIss := isBacktickAuthorizedByLoad(cat, idx, vocab, postHeld, sent.sentence, bs, bindings)
				for _, iss := range schemaIss {
					claim := iss.Path
					if claim == "" {
						claim = bs.Span
					}
					out = append(out, SubjectViolation{Label: label, Subject: SubjectSelfRequestable(), Claim: claim, Line: sent.line, Reason: iss.Reason})
				}
				if authOK {
					maskedSpans = append(maskedSpans, [2]int{bs.Start, bs.End})
				}
			}
		}
		for _, ex := range ParseCallExamples(cat, sent.sentence) {
			if isClaimSpanNegated(sent.sentence, ex.Start, ex.End) {
				s, e := expandFormattingBounds(sent.sentence, ex.Start, ex.End)
				maskedSpans = append(maskedSpans, [2]int{s, e})
			}
		}
		selfSentence := maskByteSpans(sent.sentence, maskedSpans)
		for _, name := range toolvocab.UnreachableNames(cat, selfSentence, held) {
			out = append(out, SubjectViolation{
				Label: label, Subject: subj, Claim: name, Line: sent.line,
				Reason: fmt.Sprintf("names unreachable tool, operation, or handle %q", name),
			})
		}
		out = append(out, checkCommandInvocations(vocab, auth, held, label, subj, selfSentence, sent.line)...)
		if auth.Schemas != nil || idx != nil {
			for _, iss := range ValidateAttributedClaims(cat, idx, selfSentence, offered, schemaFn) {
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
	for _, bs := range ExtractInlineBackticks(s) {
		if isClaimSpanNegated(s, bs.Start, bs.End) {
			continue
		}
		raw := strings.TrimSpace(bs.Span)
		if c.KnownName(raw) {
			seen[raw] = true
		}
		for _, t := range toolvocab.ToolsIn(c, "`"+raw+"`") {
			if c.KnownName(t) {
				seen[t] = true
			}
		}
	}
	return sortedTrueKeys(seen)
}

func hasNonNegatedMatch(text string, re *regexp.Regexp) bool {
	for _, sent := range splitActiveSentences(text) {
		for _, loc := range re.FindAllStringIndex(sent.sentence, -1) {
			if !isClaimSpanNegated(sent.sentence, loc[0], loc[1]) {
				return true
			}
		}
	}
	return false
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
