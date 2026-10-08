package assembly

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) appendWorkerLegInject(ctx context.Context, sess *api.Session, rendered string, turn *TurnAssemblyScratch) ([]api.Message, error) {
	deps := e.surface.wiring
	legCtx, err := deps.WorkerContext.BuildWorkerPromptContext(sess.ID, sess)
	if err != nil {
		return nil, err
	}
	legCtx.Checklist = FilterChecklistDedup(legCtx.Checklist, ExtractProcessBullets(rendered))
	if deps.SiblingNoteDelivery != nil {
		page, err := deps.SiblingNoteDelivery.PrepareSiblingNotes(ctx, sess.ID, siblingNoteInjectCap)
		if err != nil {
			return nil, err
		}
		legCtx.SiblingNotes = page.Notes
		legCtx.SiblingNotesMore = page.More
		legCtx.SiblingNotesAfter = page.Cursor
		turn.LastSeenSiblingNoteID = page.Cursor
		turn.PendingSiblingNotes = page.Notes
	}
	if deps.PeerReservations != nil {
		legCtx.ReservedPaths = deps.PeerReservations.PeerReservations(ctx, sess.ID)
	}
	if deps.Injects == nil {
		return nil, fmt.Errorf("worker-leg inject: inject renderer not configured")
	}
	var guidance []api.Message
	if strings.TrimSpace(legCtx.AgentsMDMessage.Content) != "" {
		guidance = append(guidance, legCtx.AgentsMDMessage)
	}
	block, err := inject.RenderWorkerLegInject(ctx, deps.Injects, sess.ID, legCtx)
	if err != nil {
		return nil, fmt.Errorf("worker-leg inject: %w", err)
	}
	if strings.TrimSpace(block) == "" {
		return guidance, nil
	}
	hostCtx := legCtx
	hostCtx.ScanDigest = nil
	hostCtx.HasPeerFindings = len(legCtx.SiblingNotes) > 0
	hostCtx.SiblingNotes = nil
	hostBlock, err := inject.RenderWorkerLegInject(ctx, deps.Injects, sess.ID, hostCtx)
	if err != nil {
		return nil, fmt.Errorf("worker-leg host projection: %w", err)
	}
	parts := workerLegContentParts(hostBlock, legCtx)
	return append(guidance, api.Message{
		Role:      api.MessageRoleSystem,
		Content:   block,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		ContentParts: parts,
	}), nil
}

func workerLegContentParts(hostBlock string, legCtx inject.WorkerLegContext) []api.MessageContentPart {
	parts := []api.MessageContentPart{{
		Content:   hostBlock,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
	}}
	if len(legCtx.ScanDigest) > 0 {
		parts = append(parts, api.MessageContentPart{
			Content: strings.Join(legCtx.ScanDigest, "\n"), Origin: api.MessageOriginRetrieval,
			Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Source: "scan_evidence",
		})
	}

	if peer := renderPeerNotesData(legCtx.SiblingNotes); peer != "" {
		parts = append(parts,
			api.MessageContentPart{
				Content: peer, Origin: api.MessageOriginPeerAgent,
				Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Source: "sibling_notes",
			},
		)
	}
	return parts
}

func renderPeerNotesData(notes []inject.SiblingNote) string {
	var lines []string
	for _, note := range notes {
		summary := strings.TrimSpace(note.Summary)
		if summary == "" {
			continue
		}
		line := summary
		if note.ID > 0 {
			line = fmt.Sprintf("finding %d: %s", note.ID, line)
		}
		if note.HasBody {
			line += " [detail: pack_board finding_id]"
		}
		if agent := strings.TrimSpace(note.Agent); agent != "" {
			line = agent + ": " + line
		}
		if ref := strings.TrimSpace(note.Ref); ref != "" {
			line += " (" + ref + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
