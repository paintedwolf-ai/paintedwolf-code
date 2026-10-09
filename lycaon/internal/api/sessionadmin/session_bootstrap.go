package sessionadmin

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// The hub is optional; without one there is no cursor to resume from.
	eventCursor := ""
	if s.Events != nil {
		eventCursor = s.Events.CurrentCursor()
	}
	sess, err := s.Store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	s.SessionView.EnrichSession(r.Context(), sess)
	transcript, err := s.Sessions.Transcript.GetTranscriptPage(r.Context(), id, wire.TranscriptPageQuery{})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if transcript.Messages == nil {
		transcript.Messages = []wire.Message{}
	}
	root := sessiontree.RootID(r.Context(), s.Store, id)
	progressContent := ""
	if s.ProgressStore != nil {
		progressContent = s.ProgressStore.Get(r.Context(), root)
	}
	progressDigest := progress.BuildDigest(progressContent, root)
	rows, err := s.Sessions.Workers.Notes.ListFindings(r.Context(), root, findings.DefaultListCap)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	findingsDigest := findings.BuildDigestFromRows(rows, root)
	queue := s.Sessions.Drafts.Snapshot(id)
	coordinator, _ := s.Sessions.Coordinator.Context.RunContext(r.Context(), id)
	workers, checkpoints, err := s.sessionBootstrapWorkersAndCheckpoints(
		r.Context(), sess.ProjectID, id,
	)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	background := s.sessionBackgroundOutputs(r.Context(), id)
	previews := []wire.PreviewAttachment{}
	if s.Preview != nil {
		previews = s.Preview.SnapshotForSession(r.Context(), id)
		if previews == nil {
			previews = []wire.PreviewAttachment{}
		}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SessionBootstrap{
		EventCursor: eventCursor,
		Activities:  s.EventPublisher.SessionActivities(id),
		Session:     *sess, Transcript: transcript,
		Progress: progressDigest, TurnClock: s.Sessions.Runner.Clocks.Read(r.Context(), root), Findings: findingsDigest, Queue: queue,
		Coordinator: coordinator, Workers: workers, Checkpoints: checkpoints,
		BackgroundOutputs: background, Previews: previews,
	})
}

func (s *Handler) sessionBootstrapWorkersAndCheckpoints(
	ctx context.Context,
	projectID string,
	sessionID string,
) ([]wire.WorkerTask, []wire.CheckpointEvent, error) {
	workers := []wire.WorkerTask{}
	if s.Workers != nil {
		listed, err := s.Workers.ListBySession(ctx, projectID, sessionID)
		if err != nil {
			return nil, nil, err
		}
		workers = append(workers, listed...)
	}
	if s.Checkpoints == nil {
		return workers, []wire.CheckpointEvent{}, nil
	}
	checkpoints, err := s.Checkpoints.ListPendingForParent(ctx, sessionID, nil)
	if err != nil {
		return nil, nil, err
	}
	return workers, checkpoints, nil
}

func (s *Handler) sessionBackgroundOutputs(ctx context.Context, sessionID string) []wire.BackgroundProcessOutput {
	processes := s.Sessions.Processes.ListBackgroundProcesses(ctx, sessionID)
	out := make([]wire.BackgroundProcessOutput, 0, len(processes))
	for _, process := range processes {
		if process.Output != nil {
			out = append(out, *process.Output)
		}
	}
	return out
}
