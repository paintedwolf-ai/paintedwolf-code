package guidance

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// Codes for assistant text withheld from the user. They inform; tools still run.
const (
	MidTurnNoteUngroundedCode     = "MID_TURN_NOTE_UNGROUNDED"
	MidTurnNoteHandleUngroundCode = "MID_TURN_NOTE_HANDLE_UNGROUND"
	MidTurnNoteURLOnlyCode        = "MID_TURN_NOTE_URL_ONLY_UNGROUND"
	MidTurnNoteArtifactCode       = "MID_TURN_NOTE_ARTIFACT_UNKNOWN"
	MidTurnNoteSyntaxCode         = "MID_TURN_NOTE_INVALID_SYNTAX"
	MidTurnNoteEmbedCode          = "MID_TURN_NOTE_MARKDOWN_EMBED"
	MidTurnNoteWaitFallbackCode   = "MID_TURN_NOTE_WAIT_FALLBACK"
	MidTurnNoteEvidenceUnreadable = "MID_TURN_NOTE_EVIDENCE_UNREADABLE"
)

// bareJSONOffender names a withheld JSON-only response in place of a node.
const bareJSONOffender = "the whole response is JSON"

// midTurnNoteFrictionCodes spend the shared grounding budget: a reference that
// names something unobserved. Uncited text is ordinary narration on a tool step,
// and charging it would spend closeout repair before any closeout exists. A
// malformed node or an artifact error makes no claim about evidence.
var midTurnNoteFrictionCodes = map[string]bool{
	MidTurnNoteHandleUngroundCode: true,
	MidTurnNoteURLOnlyCode:        true,
}

// MidTurnNoteChargesFriction reports whether withholding for code spends friction.
func MidTurnNoteChargesFriction(code string) bool { return midTurnNoteFrictionCodes[code] }

// MidTurnObservation names the OAR observation that selects a code's rule.
func MidTurnObservation(code string) string { return strings.ToLower(code) }

// MidTurnOutcome is the publication decision for one response's text.
type MidTurnOutcome string

const (
	// MidTurnSilent means the text holds nothing to say: no note and no debt.
	MidTurnSilent   MidTurnOutcome = "silent"
	MidTurnPublish  MidTurnOutcome = "publish"
	MidTurnWithhold MidTurnOutcome = "withhold"
)

// MidTurnNoteInput is one completed response's screened text and a snapshot of
// the evidence observed before its tool batch runs.
type MidTurnNoteInput struct {
	Text              string
	Roots             evidence.CitationRoots
	SurfaceID         string
	Evidence          CloseoutEvidence
	Presentable       []string
	CitationsRequired bool
	RecentHandles     []string
}

// MidTurnNoteAudit is the decision. Content and Grounding are set to publish;
// Code and Data are set to withhold.
type MidTurnNoteAudit struct {
	Outcome     MidTurnOutcome
	Content     string
	Grounding   *api.CitationGrounding
	ArtifactIDs []string
	Code        string
	Data        map[string]any
	// Attempted reports that the text supplied a citation, valid or not.
	Attempted bool
	// CitationsRequired echoes the requirement this audit applied.
	CitationsRequired bool
}

// AuditMidTurnNote validates every supplied reference, then decides whether
// citations were required. Neither step reads the prose around a reference.
func AuditMidTurnNote(in MidTurnNoteInput) MidTurnNoteAudit {
	draft := ParseCloseoutDraft(in.Text, DraftParseOptions(in.Roots, in.Evidence.Ledger))
	audit := MidTurnNoteAudit{Attempted: midTurnCitationCount(draft.Parsed) > 0 || len(draft.Issues) > 0, CitationsRequired: in.CitationsRequired}
	if IsBareJSONResponse(in.Text) {
		return audit.withhold(MidTurnNoteSyntaxCode, OffenderHintData([]string{bareJSONOffender}))
	}
	if offenders := midTurnSyntaxOffenders(draft); len(offenders) > 0 {
		return audit.withhold(MidTurnNoteSyntaxCode, OffenderHintData(offenders))
	}
	if embeds := CloseoutMarkdownArtifactEmbedIDs(draft.Parsed); len(embeds) > 0 {
		return audit.withhold(MidTurnNoteEmbedCode, OffenderHintData(embeds))
	}
	resolved := ResolveDraftCitations(in.Roots, in.SurfaceID, draft, in.Evidence, in.Presentable)
	var unknownArtifacts, unresolved []string
	for _, rejected := range resolved.Rejected() {
		if rejected.Kind == citationmd.KindArtifact {
			unknownArtifacts = append(unknownArtifacts, rejected.Offender)
			continue
		}
		unresolved = append(unresolved, rejected.Offender)
	}
	if len(unknownArtifacts) > 0 {
		data := OffenderHintData(unknownArtifacts)
		data["surface_presentable_ids"] = strings.Join(in.Presentable, ", ")
		return audit.withhold(MidTurnNoteArtifactCode, data)
	}
	if len(unresolved) > 0 {
		if len(draft.CitedEvidence) == 0 && !CloseoutLedgerHasCitableEvidence(in.Evidence) {
			return audit.withhold(MidTurnNoteURLOnlyCode, OffenderHintData(unresolved))
		}
		return audit.withhold(MidTurnNoteHandleUngroundCode, midTurnHandleData(GroundingHintData(unresolved, in.Evidence.Ledger), in.RecentHandles))
	}
	cited := midTurnCitationCount(draft.Parsed) > 0
	if !cited && in.CitationsRequired {
		return audit.withhold(MidTurnNoteUngroundedCode, midTurnHandleData(GroundingHintData(nil, in.Evidence.Ledger), in.RecentHandles))
	}
	content := strings.TrimSpace(PresentCloseoutMarkdown(draft, resolved))
	if content == "" && len(draft.ArtifactIDs) == 0 {
		audit.Outcome = MidTurnSilent
		return audit
	}
	audit.Outcome, audit.Content, audit.ArtifactIDs = MidTurnPublish, content, draft.ArtifactIDs
	if cited {
		audit.Grounding = BuildCloseoutCitationGrounding(in.Roots, in.SurfaceID, CoordinatorCompletionReport{
			Synthesis: content, CitedEvidence: draft.CitedEvidence, CitedURLs: draft.CitedURLs, Verification: draft.Verification,
		}, in.Evidence, CloseoutGroundingEval{})
		return audit
	}
	audit.Grounding = EvidenceOptionalGrounding(CoordinatorCompletionReport{Verification: draft.Verification})
	return audit
}

func (a MidTurnNoteAudit) withhold(code string, data map[string]any) MidTurnNoteAudit {
	a.Outcome, a.Code, a.Data = MidTurnWithhold, code, data
	return a
}

// midTurnHandleData leads the handle sample with this request's observations,
// which are the ones an update most likely reports.
func midTurnHandleData(data map[string]any, recent []string) map[string]any {
	if len(recent) > 0 {
		data["observed_handles_sample"] = strings.Join(recent, ", ")
	}
	return data
}

func midTurnCitationCount(doc citationmd.Document) int {
	count := 0
	for _, occurrence := range doc.Occurrences {
		switch occurrence.Kind {
		case citationmd.KindCitation, citationmd.KindWorkspaceLink, citationmd.KindWebLink:
			count++
		case citationmd.KindArtifact, citationmd.KindVerify, citationmd.KindRepair:
		}
	}
	return count
}

// midTurnSyntaxOffenders lists damaged nodes, and repair directives, which mean
// something only inside a retained closeout's repair.
func midTurnSyntaxOffenders(draft CloseoutDraft) []string {
	var offenders []string
	for _, issue := range draft.Issues {
		offenders = append(offenders, issue.Token)
	}
	for _, occurrence := range draft.Occurrences {
		if occurrence.Kind == citationmd.KindRepair {
			offenders = append(offenders, occurrence.Token)
		}
	}
	return offenders
}

// midTurnStructuralOptions parse text with no ledger at hand: registered handle
// kinds are attempts, as in the audit, and bare paths stay prose.
func midTurnStructuralOptions() citationmd.Options {
	return citationmd.Options{KnownHandleKind: KnownEvidenceKind(evidence.Ledger{})}
}

// MidTurnTextHasSpeech reports whether text says anything once markers and
// protocol nodes are gone. Marker-only and citation-only text creates no debt.
func MidTurnTextHasSpeech(text string) bool {
	return strings.TrimSpace(citationmd.Project(citationmd.Parse(text, midTurnStructuralOptions()))) != ""
}

// MidTurnTextAttemptsCitation reports a reference or damaged node in text.
func MidTurnTextAttemptsCitation(text string) bool {
	doc := citationmd.Parse(text, midTurnStructuralOptions())
	return midTurnCitationCount(doc) > 0 || len(doc.Issues) > 0
}

// IntentWindowHasCitableObservations reports whether the model holds something it
// can cite from tool rows since the current user intent: a printed handle, or a
// command output the ledger confirmed as delivered source, cited by its path. A
// command with no such output leaves nothing to cite, so it requires nothing.
func IntentWindowHasCitableObservations(history []api.Message, ledger evidence.Ledger) bool {
	for _, msg := range intentWindow(history) {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if len(msg.EvidenceHandles) > 0 {
			return true
		}
		if msg.ToolResult != nil && programDeliveredSource(msg.ToolResult.CommandProgram, ledger) {
			return true
		}
	}
	return false
}

// programDeliveredSource finds a delivered source record for one of the program's
// retained outputs. A delivery's identity starts with its output's reference.
func programDeliveredSource(program *api.CommandProgramReceipt, ledger evidence.Ledger) bool {
	if program == nil {
		return false
	}
	for _, output := range program.Outputs {
		for _, rec := range ledger.Handles {
			if rec.SourceDelivery != nil && output.Ref != "" && strings.HasPrefix(rec.SourceDelivery.DeliveryID, output.Ref+":") {
				return true
			}
		}
	}
	return false
}

// IntentWindowHandles lists this intent's newest handles first, bounded.
func IntentWindowHandles(history []api.Message, limit int) []string {
	window := intentWindow(history)
	var handles []string
	for i := len(window) - 1; i >= 0 && len(handles) < limit; i-- {
		for j := len(window[i].EvidenceHandles) - 1; j >= 0 && len(handles) < limit; j-- {
			handles = append(handles, window[i].EvidenceHandles[j])
		}
	}
	return handles
}

func intentWindow(history []api.Message) []api.Message {
	return history[api.UserIntentBoundary(history):]
}

// MidTurnEvidence unions the session and worker-leg ledgers, and reports whether a
// leg dispatched since the current intent has observations. An error is not an
// empty ledger: nothing can be decided about citations.
func MidTurnEvidence(ctx context.Context, reader CloseoutEvidenceReader, sessionID string, history []api.Message) (CloseoutEvidence, bool, error) {
	if reader == nil {
		return CloseoutEvidence{}, false, errors.New("evidence ledger not configured")
	}
	ev, err := UnionCloseoutEvidence(ctx, reader, sessionID, history)
	if err != nil {
		return CloseoutEvidence{}, false, err
	}
	legs, err := CurrentIntentLegEvidence(ctx, reader, sessionID, history)
	if err != nil {
		return CloseoutEvidence{}, false, err
	}
	return ev, CloseoutLedgerHasCitableEvidence(legs), nil
}

// CurrentIntentLegEvidence is the evidence of worker legs dispatched since the
// current user intent.
func CurrentIntentLegEvidence(ctx context.Context, reader CloseoutEvidenceReader, sessionID string, history []api.Message) (CloseoutEvidence, error) {
	legs, err := reader.WorkerLegs(ctx, strings.TrimSpace(sessionID), userIntentTime(history))
	if err != nil {
		return CloseoutEvidence{}, err
	}
	return UnionLegEvidence(ctx, reader, legs)
}

// MidTurnWorkerFact is one worker's state as dispatch and result rows record it.
type MidTurnWorkerFact struct {
	AgentType string `json:"agent_type"`
	Status    string `json:"status"`
}

// Worker states the host reports. A summary row's own status replaces started.
const midTurnWorkerStarted = "started"

// IntentWindowWorkerFacts lists this intent's workers from structured rows, in
// dispatch order. No text from a worker or the model enters it.
func IntentWindowWorkerFacts(history []api.Message) []MidTurnWorkerFact {
	var facts []MidTurnWorkerFact
	byWorker := map[string]int{}
	for _, msg := range intentWindow(history) {
		if msg.ToolResult != nil && msg.ToolResult.Dispatch != nil && strings.TrimSpace(msg.ToolResult.Dispatch.WorkerID) != "" {
			dispatch := msg.ToolResult.Dispatch
			byWorker[dispatch.WorkerID] = len(facts)
			facts = append(facts, MidTurnWorkerFact{AgentType: dispatch.AgentType, Status: midTurnWorkerStarted})
		}
		if summary := msg.WorkerSummary; summary != nil {
			if i, ok := byWorker[summary.WorkerID]; ok {
				facts[i].Status = string(summary.Status)
				continue
			}
			byWorker[summary.WorkerID] = len(facts)
			facts = append(facts, MidTurnWorkerFact{AgentType: summary.AgentType, Status: string(summary.Status)})
		}
	}
	return facts
}
