package assembly

import (
	"context"
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
