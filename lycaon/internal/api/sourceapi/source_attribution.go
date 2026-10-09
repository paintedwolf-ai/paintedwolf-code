package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func mapSourceContributors(authors []sourceledger.Contributor) []wire.SourceContributor {
	result := make([]wire.SourceContributor, 0, len(authors))
	for _, author := range authors {
		result = append(result, wire.SourceContributor{Origin: author.Origin, SessionID: author.SessionID,
			Turn: author.Turn, PersonID: author.PersonID, ActorLabel: author.ActorLabel,
			ToolCallID: author.ToolCallID, ToolName: author.ToolName, WorkerID: author.JobID})
	}
	return result
}

func mapSourceAttribution(attribution *sourceledger.ComparisonAttribution) *wire.SourceComparisonAttribution {
	if attribution == nil {
		return nil
	}
	return &wire.SourceComparisonAttribution{Before: mapAttributedText(attribution.Before), After: mapAttributedText(attribution.After)}
}

func mapAttributedText(runs []sourceledger.AttributedText) []wire.SourceAttributedText {
	result := make([]wire.SourceAttributedText, 0, len(runs))
	for _, run := range runs {
		result = append(result, wire.SourceAttributedText{Index: run.Index, Length: run.Length,
			Contributors: mapSourceContributors(run.Contributors), Selected: run.Selected, Visible: run.Visible})
	}
	return result
}

func (s *Workspace) workspaceSourceHead(ctx context.Context, p *project.Project, fileID string) (sourceledger.BranchHead, sourcebranch.ID, error) {
	branches := make(map[sourcebranch.ID]bool)
	for _, root := range p.Roots {
		branch := p.BranchForRoot(root.ID)
		if branches[branch] {
			continue
		}
		branches[branch] = true
		head, err := s.SourceLedger.ResolveHeadByFile(ctx, p.ID, branch, fileID)
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			continue
		}
		if err != nil {
			return sourceledger.BranchHead{}, sourcebranch.Trunk, err
		}
		for _, candidate := range p.Roots {
			if candidate.ID == head.RootID && p.BranchForRoot(candidate.ID) == branch {
				return head, branch, nil
			}
		}
	}
	return sourceledger.BranchHead{}, sourcebranch.Trunk, sourceledger.ErrHistoryNotFound
}

func workspaceSourceBranches(p *project.Project) map[string]sourcebranch.ID {
	branches := make(map[string]sourcebranch.ID, len(p.Roots))
	for _, root := range p.Roots {
		branches[root.ID] = p.BranchForRoot(root.ID)
	}
	return branches
}

func (s *History) HandleGetProjectSourceAttribution(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	// Worktree-bound chats resolve attribution in their own workspace.
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	rootID := strings.TrimSpace(r.URL.Query().Get("root_id"))
	if rootID == "" {
		s.responses.InvalidQueryParam(w, "root_id", "is required")
		return
	}
	if path == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	res, err := s.SourceLedger.QueryAttribution(r.Context(), p.ID, p.BranchForRoot(rootID), rootID, path)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	intervals := make([]wire.SourceAttributionInterval, 0, len(res.Intervals))
	for _, iv := range res.Intervals {
		// The attribution view highlights only agent-authored lines.
		if iv.Origin != wire.SourceChangeOriginAgent {
			continue
		}
		row := wire.SourceAttributionInterval{
			StartLine:  iv.StartLine,
			EndLine:    iv.EndLine,
			Turn:       iv.Turn,
			RecordedAt: iv.TS,
		}
		if iv.SessionID != "" {
			row.SessionID = &iv.SessionID
		}
		if iv.ToolCallID != "" {
			row.ToolCallID = &iv.ToolCallID
		}
		intervals = append(intervals, row)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceAttributionResponse{
		HeadSha256: res.HeadSHA256,
		Intervals:  intervals,
	})
}

func (s *History) HandleGetWorkerChanges(w http.ResponseWriter, r *http.Request) {
	workerID := strings.TrimSpace(chi.URLParam(r, "id"))
	task, ok := s.Workers.Get(workerID)
	if !ok || task == nil {
		s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
		return
	}
	rows, err := s.SourceLedger.QueryJobChanges(r.Context(), task.ProjectID, workerID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	files := make([]wire.WorkerJobChangedFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, wire.WorkerJobChangedFile{RootID: row.RootID, Path: row.Path, Op: row.Op})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.WorkerJobChangesResponse{Files: files})
}
