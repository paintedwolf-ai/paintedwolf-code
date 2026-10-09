package assembly

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

// siblingNoteInjectCap bounds how many fresh peer notes ride one worker turn.
const siblingNoteInjectCap = 8

// WorkerContextBuilder loads leg metadata for child session prompts.
type WorkerContextBuilder interface {
	BuildWorkerPromptContext(childSessionID string, sess *api.Session) (inject.WorkerLegContext, error)
}

// SiblingNoteDeliveryRecorder prepares retained context and commits it after a response.
type SiblingNoteDeliveryRecorder interface {
	PrepareSiblingNotes(context.Context, string, int) (inject.SiblingNotePage, error)
	CommitSiblingNotes(context.Context, string, string, int64, []inject.SiblingNote) error
}

// PeerReservationSource returns active handoff_reserve holds from sibling workers.
type PeerReservationSource interface {
	PeerReservations(ctx context.Context, childSessionID string) []inject.ReservedPath
}

// ExtractProcessBullets returns list items from a rendered Process section.
func ExtractProcessBullets(rendered string) []string {
	section := extractMarkdownSection(rendered, "## Process", "## Tools")
	if section == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "0123456789.")
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// FilterChecklistDedup drops L3 checklist lines that duplicate L1 process bullets.
func FilterChecklistDedup(checklist, processBullets []string) []string {
	if len(checklist) == 0 || len(processBullets) == 0 {
		return checklist
	}
	out := make([]string, 0, len(checklist))
	for _, item := range checklist {
		itemNorm := strings.ToLower(strings.TrimSpace(item))
		dup := false
		for _, proc := range processBullets {
			if itemNorm != "" && itemNorm == strings.ToLower(strings.TrimSpace(proc)) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out = append(out, item)
	}
	return out
}

func extractMarkdownSection(text, startHeading, endHeading string) string {
	start := strings.Index(text, startHeading)
	if start < 0 {
		return ""
	}
	start += len(startHeading)
	rest := text[start:]
	if end := strings.Index(rest, endHeading); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

// appendWorkerLegInject builds changed worker context for the turn.
func (e *AssemblyEngine) appendWorkerLegInject(ctx context.Context, sess *api.Session, rendered string, turn *TurnAssemblyScratch) ([]api.Message, error) {
	deps := e.deps()
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
