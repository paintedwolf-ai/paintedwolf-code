package editordoc

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// AgentCall names the agent tool call that caused a save.
type AgentCall struct {
	SessionID  string
	Turn       int
	ToolCallID string
	ToolName   string
}

// SaveForAgentOperation saves drafts before mutation, preserving authorship.
// A draft whose disk base changed returns an ExecutionConflict.
func (s *Service) SaveForAgentOperation(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string, descendants bool, call AgentCall) error {
	if s == nil || s.store == nil {
		return nil
	}
	p, err := s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return err
	}
	return s.VisitUnsavedDocuments(ctx, p.ID, branch, rootID, path, descendants, func(state DocumentPathState) error {
		return s.saveForAgent(ctx, p, state.ID, call)
	})
}

func (s *Service) saveForAgent(ctx context.Context, p *project.Project, documentID string, call AgentCall) error {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(documentID))
	defer unlock()
	current, err := s.checked(ctx, documentID, p.ID)
	if err != nil {
		return err
	}
	if !current.Dirty || current.BranchID != p.BranchForRoot(current.RootID) {
		return nil
	}
	if current.Diverged {
		return s.fileChangedOutside(p, current)
	}
	saved, err := s.saveLocked(ctx, p, current, uuid.NewString(), saveActor{
		Origin:     api.SourceChangeOriginAgent,
		SessionID:  strings.TrimSpace(call.SessionID),
		Turn:       call.Turn,
		ToolCallID: strings.TrimSpace(call.ToolCallID),
		ToolName:   strings.TrimSpace(call.ToolName),
	})
	if errors.Is(err, projectsource.ErrSourceWriteConflict) {
		return s.fileChangedOutside(p, current)
	}
	if err != nil {
		return err
	}
	if saved != nil {
		s.agentReads.replaceRevision(p.ID, current.ID, current.Revision, saved.Revision)
	}
	return nil
}

func (s *Service) fileChangedOutside(p *project.Project, d *Document) error {
	pinned := ExecutionPin{Revision: d.Revision, DiskSHA256: d.BaseSHA256, Absent: d.Absent}
	if d.Absent {
		pinned.DiskSHA256 = ""
	}
	observed := ExecutionPin{Revision: d.Revision, Absent: true}
	read, err := readDocumentSource(p, d)
	switch {
	case errors.Is(err, projectsource.ErrSourceNotFound):
	case err != nil:
		return err
	default:
		observed.Absent, observed.DiskSHA256 = false, read.SHA256
	}
	return &ExecutionConflict{DocumentID: d.ID, RootID: d.RootID, Path: d.Path, Pinned: pinned, Current: observed}
}
