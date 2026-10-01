package board

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// BuildView implements events.BoardViewSource for SSE board topic events.
func (b *SnapshotBuilder) BuildView(ctx context.Context, projectID, workspacePath, sessionID string, level api.BoardDetailLevel) (api.BoardView, error) {
	var roots []projectroot.RootRef
	if b != nil && b.Projects != nil && strings.TrimSpace(projectID) != "" {
		if p, err := b.Projects.Get(ctx, projectID); err == nil && p != nil {
			roots = project.RootRefsFrom(p)
		}
	}
	snap, err := b.Build(ctx, projectID, workspacePath, sessionID, level, roots)
	if err != nil {
		return api.BoardView{}, err
	}
	return BuildBoardView(snap, sessionID, level, time.Now().UTC()), nil
}

// BuildBoardView formats an internal snapshot for HTTP, SSE, and pack_board consumers.
func BuildBoardView(snapshot *api.BoardSnapshot, sessionID string, level api.BoardDetailLevel, now time.Time) api.BoardView {
	if snapshot == nil {
		return api.BoardView{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if level == "" {
		level = api.BoardDetailLevelCompact
	}
	snap := *snapshot
	snap.DetailLevel = level
	formatter := &CompactFormatter{}
	var truncated []string
	boardText := formatter.FormatWithOpts(snap, FormatOpts{
		MaxChars:          maxCharsForLevel(level),
		Now:               now,
		TruncatedSections: &truncated,
	})

	tasks := packboard.WorkerTasksFromSnapshot(snap)
	view := api.BoardView{
		SessionID:         strings.TrimSpace(sessionID),
		Summary:           BuildBoardSummary(snap),
		Repo:              snap.Repo,
		Git:               snap.Git,
		Scans:             snap.Scans,
		Cost:              snap.Cost,
		PackContentHash:   snap.PackContentHash,
		DetailLevel:       level,
		Board:             boardText,
		BoardChars:        len(boardText),
		Truncated:         len(truncated) > 0,
		TruncatedSections: truncated,
		GeneratedAt:       now.UTC().Format(time.RFC3339),
		NowLine:           packboard.FormatNowLine(now),
	}
	if level != api.BoardDetailLevelStatus {
		roster := BuildWorkerRoster(tasks, level)
		roster = AttachRosterReservations(roster, packboard.ReservationsFromSnapshot(snap))
		view.Roster = roster
		view.PromotePaths = packboard.PromotePathsFromSnapshot(snap)
		view.Delegation = BuildDelegationRoster(packboard.DelegationsFromSnapshot(snap), level)
		view.ActiveWorkflowRun = snap.ActiveWorkflowRun
	}
	if level == api.BoardDetailLevelForensic && len(tasks) > 0 {
		forensic := append([]api.WorkerTask(nil), tasks...)
		for i := range forensic {
			forensic[i].WorkspaceRoot = "" // pack_board omits the absolute branch root
		}
		view.ForensicWorkers = &api.BoardForensicWorkers{
			Notice: api.BoardForensicNotice,
			Tasks:  forensic,
		}
	}
	return view
}

func maxCharsForLevel(level api.BoardDetailLevel) int {
	switch level {
	case api.BoardDetailLevelFull:
		return api.MaxBoardDetailChars
	default:
		return api.MaxBoardCompactChars
	}
}
