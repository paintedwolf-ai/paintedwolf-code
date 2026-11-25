package toolvocabtest

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/prompts"
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
	SubjectKindNamedAgent            SubjectKind = "named_agent"
	SubjectKindFollowupLeg           SubjectKind = "followup_leg"
	SubjectKindSessionNextTurn       SubjectKind = "session_next_turn"
	SubjectKindHistoricalObservation SubjectKind = "historical_observation"
)

// ClaimModality classifies how a claim asserts capability or historical state.
type ClaimModality string

const (
	ModalityCallableNow  ClaimModality = "callable_now"
	ModalityDelegated    ClaimModality = "delegated"
	ModalityDeferredTurn ClaimModality = "deferred_turn"
	ModalityHistorical   ClaimModality = "historical"
)

// ClaimSubject identifies the actor, time, and modality a claim describes,
// as fixed by the template, include, or roster entry that produced the block.
// Writes records that the producer rendered the block for a subject it holds
// to be write-capable.
type ClaimSubject struct {
	Kind     SubjectKind
	Modality ClaimModality
	AgentID  string
	Origin   api.MessageOrigin
	Writes   bool
}

// SubjectSelfNow returns the default current-turn subject for the active reader.
func SubjectSelfNow() ClaimSubject {
	return ClaimSubject{Kind: SubjectKindSelfNow, Modality: ModalityCallableNow, Origin: api.MessageOriginHost}
}

// SubjectNamedAgent returns the delegated subject for a named worker agent profile.
func SubjectNamedAgent(agentID string) ClaimSubject {
	return ClaimSubject{Kind: SubjectKindNamedAgent, Modality: ModalityDelegated, AgentID: strings.TrimSpace(agentID), Origin: api.MessageOriginHost}
}

// SubjectFollowupLeg returns the subject for worker-closeout follow-up leg
// recommendations; writes is the mutation fact the producer rendered them with.
func SubjectFollowupLeg(writes bool) ClaimSubject {
	return ClaimSubject{Kind: SubjectKindFollowupLeg, Modality: ModalityDeferredTurn, Origin: api.MessageOriginHost, Writes: writes}
}

// SubjectSessionNextTurn returns the subject for coordinator-turn-closeout
// next-turn advice; writes is the mutation fact the producer rendered it with.
func SubjectSessionNextTurn(writes bool) ClaimSubject {
	return ClaimSubject{Kind: SubjectKindSessionNextTurn, Modality: ModalityDeferredTurn, Origin: api.MessageOriginHost, Writes: writes}
}

// SubjectHistoricalObservation returns the subject for historical observations or non-host quotes.
func SubjectHistoricalObservation(origin api.MessageOrigin) ClaimSubject {
	return ClaimSubject{Kind: SubjectKindHistoricalObservation, Modality: ModalityHistorical, Origin: origin}
}

// ActorAuthority captures a resolved actor's prompt Reader, program reach, and projected schemas.
type ActorAuthority struct {
	Reader           prompts.Reader
	ProgramReach     toolreach.Programs
	ProgramTable     *toolreach.ProgramTable
	PostLoadPrograms *toolreach.ProgramTable
	Metas            []tools.ToolMeta
	Schemas          map[string]map[string]any
	PostLoadSchemas  map[string]map[string]any
}

// AuthorityOption configures optional production projections on ActorAuthority.
type AuthorityOption func(*ActorAuthority)

// WithProgramTable attaches a production program and effect-mode admission table.
func WithProgramTable(table *toolreach.ProgramTable) AuthorityOption {
	return func(a *ActorAuthority) {
		a.ProgramTable = table
	}
}

// WithPostLoadAuthority attaches production deferred-tool projections and post-load program admission.
func WithPostLoadAuthority(schemas map[string]map[string]any, programs *toolreach.ProgramTable) AuthorityOption {
	return func(a *ActorAuthority) {
		a.PostLoadSchemas = maps.Clone(schemas)
		a.PostLoadPrograms = programs
	}
}

// AuthorityFromReaderAndMetas constructs an ActorAuthority from a Reader, ProgramReach, and ToolMetas.
func AuthorityFromReaderAndMetas(reader prompts.Reader, progReach toolreach.Programs, metas []tools.ToolMeta, opts ...AuthorityOption) ActorAuthority {
	schemas := make(map[string]map[string]any, len(metas))
	for _, m := range metas {
		schemas[m.Name] = m.ArgsSchema
	}
	if progReach == nil {
		progReach = reader.CanReachProgram
	}
	auth := ActorAuthority{
		Reader:       reader,
		ProgramReach: progReach,
		Metas:        slices.Clone(metas),
		Schemas:      schemas,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&auth)
		}
	}
	return auth
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

// ClaimBlock pairs host-authored text with the ClaimSubject its producer fixes.
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

var historicalHandleRe = regexp.MustCompile(`\[([a-z][a-z0-9_]*)#[0-9Nn]+\]`)

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
	g := func(auth ActorAuthority) claimGrammar {
		return newClaimGrammar(cat, evalCtx.Prunable, vocab, auth)
	}
	switch block.Subject.Kind {
	case SubjectKindHistoricalObservation:
		return evaluateHistoricalBlock(vocab, block)
	case SubjectKindNamedAgent:
		return evaluateNamedAgentBlock(evalCtx, g, block)
	case SubjectKindFollowupLeg:
		return evaluateDeferredTurnBlock(evalCtx.FollowupLeg, g, block)
	case SubjectKindSessionNextTurn:
		return evaluateDeferredTurnBlock(evalCtx.SessionNextTurn, g, block)
	default:
		return g(evalCtx.Self).evaluate(block)
	}
}

func evaluateHistoricalBlock(vocab *BacktickVocab, block ClaimBlock) []SubjectViolation {
	if block.Subject.Origin != "" && block.Subject.Origin != api.MessageOriginHost {
		return nil
	}
	var out []SubjectViolation
	knownKinds := evidence.ActiveBinding().KindShapeMap()
	seenKinds := map[string]bool{}
	cite := func(kind string, line int) {
		if _, ok := knownKinds[kind]; ok || kind == "kind" || seenKinds[kind] {
			return
		}
		seenKinds[kind] = true
		out = append(out, SubjectViolation{
			Label: block.Label, Subject: block.Subject, Claim: kind + "#", Line: line,
			Reason: fmt.Sprintf("historical observation cites undeclared receipt kind %q", kind),
		})
	}
	spans := ExtractInlineBackticks(block.Text)
	for _, bs := range spans {
		if m := handleSpanRe.FindStringSubmatch(bs.Span); len(m) == 2 {
			cite(m[1], bs.Line)
		}
	}
	for _, m := range historicalHandleRe.FindAllStringSubmatchIndex(block.Text, -1) {
		cite(block.Text[m[2]:m[3]], 1+strings.Count(block.Text[:m[0]], "\n"))
	}
	if vocab == nil {
		return out
	}
	for _, bs := range spans {
		head, field, hasDot := strings.Cut(normalizeSchemaPath(bs.Span), ".")
		if hasDot && (head == "receipt" || head == "proof_json" || head == "report_json") && !vocab.catalogIDs[field] && !vocab.schemaItems[field] {
			out = append(out, SubjectViolation{
				Label: block.Label, Subject: block.Subject, Claim: bs.Span, Line: bs.Line,
				Reason: fmt.Sprintf("historical observation cites undeclared receipt field %q", bs.Span),
			})
		}
	}
	return out
}

// evaluateNamedAgentBlock checks a roster entry against the named agent's
// resolved reader. The agent definition's mutation capability is the entry's
// write claim.
func evaluateNamedAgentBlock(evalCtx ClaimEvaluationContext, g func(ActorAuthority) claimGrammar, block ClaimBlock) []SubjectViolation {
	agentID := block.Subject.AgentID
	writes, _ := prompts.AgentMutationCapable(agentID)
	if evalCtx.SessionMode == api.SessionModeAsk && writes {
		return []SubjectViolation{{
			Label: block.Label, Subject: block.Subject, Claim: agentID, Line: 1,
			Reason: fmt.Sprintf("Ask mode cannot offer mutating worker agent %q", agentID),
		}}
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
		targetAuth, err = ResolveWorkerAuthorityForSession(context.Background(), nil, mode, posture, 1, true, false, true, false, agentID)
	}
	if err != nil {
		return []SubjectViolation{{Label: block.Label, Subject: block.Subject, Claim: agentID, Line: 1, Reason: err.Error()}}
	}
	var out []SubjectViolation
	if writes && !readerAllowsMutation(targetAuth.Reader) {
		out = append(out, SubjectViolation{
			Label: block.Label, Subject: block.Subject, Claim: "write_capability", Line: 1,
			Reason: fmt.Sprintf("roster offers mutating agent %q, whose resolved Reader cannot write under mode=%q posture=%q", agentID, evalCtx.SessionMode, evalCtx.Posture),
		})
	}
	return append(out, g(targetAuth).evaluate(block)...)
}

func evaluateDeferredTurnBlock(target *ActorAuthority, g func(ActorAuthority) claimGrammar, block ClaimBlock) []SubjectViolation {
	if target == nil {
		return []SubjectViolation{{Label: block.Label, Subject: block.Subject, Reason: fmt.Sprintf("missing %s authority", block.Subject.Kind), Line: 1}}
	}
	var out []SubjectViolation
	if block.Subject.Writes && !readerAllowsMutation(target.Reader) {
		out = append(out, SubjectViolation{
			Label: block.Label, Subject: block.Subject, Claim: "command_mutation", Line: 1,
			Reason: fmt.Sprintf("%s was rendered as write-capable, which the target Reader is not", block.Subject.Kind),
		})
	}
	return append(out, g(*target).evaluate(block)...)
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
