package promptloop

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	midTurnHostProgressKick = "coordinator-mid-turn-host-progress"
	midTurnHandleSample     = 6
)

// publishMidTurnText decides the fate of a carrier's text before its first tool
// runs. It never stops the tools: only a storage failure on a note it means to
// publish ends the batch, because claiming publication would be false.
func (l *toolBatch) publishMidTurnText(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	carrierID, surfaceID string,
	toolCalls []api.ToolCall,
	lastToolTS *time.Time,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	carrierID = strings.TrimSpace(carrierID)
	if sess == nil || sess.IsWorkerChild() || carrierID == "" || !surface.PublishesMidTurnText(surfaceID) {
		return history, nil
	}
	audit := l.auditCarrier(ctx, sess, sessionID, history, carrierID, surfaceID)
	if audit.Outcome == guidance.MidTurnPublish {
		note := api.Message{
			ID: api.MidTurnNoteID(carrierID), Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote,
			Visibility: api.MessageVisibilityTranscript, Content: audit.Content,
			Grounding: audit.Grounding, ArtifactIDs: audit.ArtifactIDs,
			SourceContext: sourceref.Mentioned(modelSourceContext(st), audit.Content),
		}
		var err error
		if history, err = l.commitMidTurnNote(ctx, sessionID, history, note, lastToolTS, st); err != nil {
			return history, err
		}
	}
	if audit.Outcome == guidance.MidTurnWithhold && guidance.MidTurnNoteChargesFriction(audit.Code) && l.Closeout.Deps.RecordGroundingFriction != nil {
		l.Closeout.Deps.RecordGroundingFriction(ctx, sessionID)
	}
	if slices.ContainsFunc(toolCalls, func(call api.ToolCall) bool { return call.Name == "wait" }) && guard.DeriveWaitVisibility(history).NeedsFallback() {
		return l.settleExhaustedVisibility(ctx, sess, sessionID, history, carrierID, surfaceID, lastToolTS, st)
	}
	if audit.Outcome == guidance.MidTurnWithhold {
		l.notifyMidTurnWithheld(ctx, sess, sessionID, surfaceID, audit.Code, audit.Data, history, audit.Attempted)
	}
	return history, nil
}

func modelSourceContext(st *promptLoopTurnState) *api.SourceContext {
	if st == nil {
		return nil
	}
	return st.output.sourceContext
}

// auditCarrier reads the screened revision held on the carrier and a snapshot
// of the evidence observed so far. A note already derived from this carrier is
// settled; evidence that cannot be read withholds rather than exempting.
func (l *toolBatch) auditCarrier(ctx context.Context, sess *api.Session, sessionID string, history []api.Message, carrierID, surfaceID string) guidance.MidTurnNoteAudit {
	noteID := api.MidTurnNoteID(carrierID)
	text := ""
	for _, msg := range history {
		if msg.ID == noteID {
			return guidance.MidTurnNoteAudit{Outcome: guidance.MidTurnSilent}
		}
		if msg.ID == carrierID {
			text = hostmarker.StripParrotedTransportMarkers(msg.Content)
		}
	}
	if strings.TrimSpace(text) == "" {
		return guidance.MidTurnNoteAudit{Outcome: guidance.MidTurnSilent}
	}
	ev, legsCite, err := midTurnEvidence(ctx, l.Closeout.Deps.EvidenceLedger, sessionID, history)
	if err != nil {
		slog.WarnContext(ctx, "mid-turn note evidence unavailable; text stays unpublished", "session_id", sessionID, "error", err)
		return guidance.MidTurnNoteAudit{Outcome: guidance.MidTurnSilent}
	}
	return guidance.AuditMidTurnNote(guidance.MidTurnNoteInput{
		Text:              text,
		Roots:             l.Context.citationRoots(ctx, sess),
		SurfaceID:         surfaceID,
		Evidence:          ev,
		Presentable:       guidance.PresentableVisualArtifactIDs(history),
		CitationsRequired: (legsCite || guidance.IntentWindowHasCitableObservations(history)) && guidance.CloseoutLedgerHasCitableEvidence(ev),
		RecentHandles:     guidance.IntentWindowHandles(history, midTurnHandleSample),
	})
}

// midTurnEvidence unions the session and worker-leg ledgers, and reports whether
// a leg dispatched since the current intent has observations.
func midTurnEvidence(ctx context.Context, reader guidance.CloseoutEvidenceReader, sessionID string, history []api.Message) (guidance.CloseoutEvidence, bool, error) {
	if reader == nil {
		return guidance.CloseoutEvidence{}, false, errors.New("evidence ledger not configured")
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, reader, sessionID, history)
	if err != nil {
		return guidance.CloseoutEvidence{}, false, err
	}
	legs, err := guidance.CurrentIntentLegEvidence(ctx, reader, sessionID, history)
	if err != nil {
		return guidance.CloseoutEvidence{}, false, err
	}
	return ev, guidance.CloseoutLedgerHasCitableEvidence(legs), nil
}

// commitMidTurnNote persists through the storage-safe path. A duplicate means an
// earlier attempt already committed this note.
func (l *toolBatch) commitMidTurnNote(ctx context.Context, sessionID string, history []api.Message, note api.Message, lastToolTS *time.Time, st *promptLoopTurnState) ([]api.Message, error) {
	stampCommitOrderTS(&note, lastToolTS)
	stored, transient := l.Projection.storageSafeMessage(ctx, note)
	if err := l.Projection.persistStorageSafeMessages(ctx, sessionID, []api.Message{stored}); err != nil && !errors.Is(err, store.ErrDuplicateMessageID) {
		return history, err
	}
	return foldStorageSafeMessages(history, []api.Message{stored}, []*api.Message{transient}, st), nil
}

// settleExhaustedVisibility ends visibility repair for a wait, once per debt. The
// host reports worker state at most once per intent, so a model that never cites
// cannot turn repair into a stream of host speech; otherwise the wait goes ahead
// silently. Either way the draft stays unpublished and the model is told.
func (l *toolBatch) settleExhaustedVisibility(ctx context.Context, sess *api.Session, sessionID string, history []api.Message, carrierID, surfaceID string, lastToolTS *time.Time, st *promptLoopTurnState) ([]api.Message, error) {
	body := ""
	if !hostProgressPublished(history) {
		body = l.midTurnHostProgress(ctx, history)
	}
	if body != "" {
		note := api.Message{
			ID: hostProgressNoteID(carrierID), Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote,
			Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
			Visibility: api.MessageVisibilityTranscript, Content: body,
			Grounding: &api.CitationGrounding{EvidenceOptional: true},
		}
		var err error
		if history, err = l.commitMidTurnNote(ctx, sessionID, history, note, lastToolTS, st); err != nil {
			return history, err
		}
	}
	slog.InfoContext(ctx, "mid-turn visibility repair exhausted", "session_id", sessionID, "surface", surfaceID)
	l.notifyMidTurnWithheld(ctx, sess, sessionID, surfaceID, guidance.MidTurnNoteWaitFallbackCode, nil, history, false)
	return history, nil
}

// hostProgressNoteID differs from the carrier's derived note, so the model reads
// this row: it holds the host's words, which no carrier repeats.
func hostProgressNoteID(carrierID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("agent-note-host:"+carrierID)).String()
}

// hostProgressPublished finds this intent's host note by its derived ID.
func hostProgressPublished(history []api.Message) bool {
	window := history[api.UserIntentBoundary(history):]
	derived := make(map[string]struct{})
	for _, msg := range window {
		if msg.Role == api.MessageRoleAssistant && len(msg.ToolCalls) > 0 {
			derived[hostProgressNoteID(msg.ID)] = struct{}{}
		}
	}
	return slices.ContainsFunc(window, func(msg api.Message) bool {
		_, ok := derived[msg.ID]
		return ok
	})
}

// midTurnHostProgress renders only structured dispatch and result facts.
func (l *toolBatch) midTurnHostProgress(ctx context.Context, history []api.Message) string {
	workers := guidance.IntentWindowWorkerFacts(history)
	if len(workers) == 0 || l.Closeout.Deps.RenderHostKick == nil {
		return ""
	}
	body, err := l.Closeout.Deps.RenderHostKick(ctx, midTurnHostProgressKick, map[string]any{"workers": workers})
	if err != nil {
		slog.WarnContext(ctx, "render mid-turn host progress", "error", err)
		return ""
	}
	return strings.TrimSpace(body)
}

// notifyMidTurnWithheld queues one structured notice for the next model request.
// The notice is advisory, so a delivery failure never stops the tools.
func (l *toolBatch) notifyMidTurnWithheld(ctx context.Context, sess *api.Session, sessionID, surfaceID, code string, data map[string]any, history []api.Message, attempted bool) {
	if l.Closeout.Deps.EvaluateMidTurnNote == nil {
		return
	}
	gc := oar.NewGuardContext()
	gc.Session.SessionID = sessionID
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	gc.Session.MidTurnCitationsRequired = guidance.IntentWindowHasCitableObservations(history)
	gc.Session.MidTurnCitationAttempted = attempted
	gc.Rejection.RejectObservation = guidance.MidTurnObservation(code)
	gc.PutRejectData(code, data)
	if err := l.Closeout.Deps.EvaluateMidTurnNote(ctx, sess, gc); err != nil {
		slog.WarnContext(ctx, "mid-turn note feedback", "session_id", sessionID, "code", code, "error", err)
	}
}
