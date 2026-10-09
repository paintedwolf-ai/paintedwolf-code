package sessionadmin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleResolveMessageNavigation(w http.ResponseWriter, r *http.Request) {
	var req wire.ResolveMessageNavigationRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.MessageID == "" || len(req.ContentSHA256) != 64 || len(req.ReferenceID) > 128 {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "message_id and content_sha256 are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	sessionID := chi.URLParam(r, "id")
	if _, err := s.Store.Get(ctx, sessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	result, err := s.Sessions.Runner.Transcript.ResolveNavigation(ctx, sessionID, req, s.ResolveNavigationWorkspace)
	switch {
	case errors.Is(err, transcript.ErrNavigationCandidateInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "candidate_index must select a stored ambiguous reference")
		return
	case errors.Is(err, transcript.ErrNavigationMessageNotProse):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "only visible assistant messages carry navigation references")
		return
	case errors.Is(err, store.ErrNavigationContentChanged):
		s.responses.Fail(w, wire.ApiErrorCodeMessageReferencesChanged, "message changed before its references resolved")
		return
	case errors.Is(err, store.ErrMessageNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeMessageNotFound, "That message is not in this chat.")
		return
	case err != nil:
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func (s *Handler) ResolveNavigationWorkspace(ctx context.Context, p *project.Project, msg wire.Message, refs []wire.NavigationReference) []wire.NavigationReference {
	out := append(make([]wire.NavigationReference, 0, len(refs)), refs...)
	groups := map[string][]int{}
	for i, ref := range refs {
		if ref.Status == wire.NavigationAmbiguous {
			continue
		}
		jobID := ref.WorkerID
		if jobID == "" && ref.Status == wire.NavigationPending {
			jobID = msg.WorkerID
		}
		groups[jobID] = append(groups[jobID], i)
	}
	for jobID, positions := range groups {
		batch := make([]wire.NavigationReference, 0, len(positions))
		for _, position := range positions {
			batch = append(batch, refs[position])
		}
		resolved := s.ResolveNavigationJob(ctx, p, jobID, batch)
		for i, position := range positions {
			out[position] = resolved[i]
		}
	}
	return out
}

func (s *Handler) ResolveNavigationJob(ctx context.Context, p *project.Project, jobID string, refs []wire.NavigationReference) []wire.NavigationReference {
	if jobID == "" {
		return s.Sources.ResolveNavigationPaths(ctx, p, sourcebranch.Trunk, refs)
	}
	unavailable := func() []wire.NavigationReference {
		out := append(make([]wire.NavigationReference, 0, len(refs)), refs...)
		for i := range out {
			out[i].Status = wire.NavigationUnavailable
			out[i].Deleted = false
			out[i].WorkerID = jobID
			out[i].Candidates = nil
		}
		return out
	}
	if s.Workers == nil {
		return unavailable()
	}
	task, ok := s.Workers.Get(jobID)
	if !ok || task == nil || task.ProjectID != p.ID {
		return unavailable()
	}
	if !task.EffectiveScope().IsWrite() {
		out := s.Sources.ResolveNavigationPaths(ctx, p, sourcebranch.Trunk, refs)
		for i := range out {
			out[i].WorkerID = ""
			for j := range out[i].Candidates {
				out[i].Candidates[j].WorkerID = ""
			}
		}
		return out
	}
	_, lease, err := s.Workers.EnsureWorkerBranch(ctx, jobID)
	if err != nil || lease == nil {
		return unavailable()
	}
	defer lease.Release()
	scoped, err := sourceapi.SourceProjectInBranch(p, lease.Root)
	if err != nil {
		return unavailable()
	}
	branch, err := sourcebranch.ForWorker(jobID)
	if err != nil {
		return unavailable()
	}
	out := s.Sources.ResolveNavigationPaths(ctx, scoped, branch, refs)
	for i := range out {
		out[i].WorkerID = jobID
		for j := range out[i].Candidates {
			out[i].Candidates[j].WorkerID = jobID
		}
	}
	return out
}
