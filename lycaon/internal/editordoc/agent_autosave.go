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

// UnsavedCause says why a launch save left a draft unsaved.
type UnsavedCause string

const (
	// UnsavedChangedOutside is a draft whose file changed outside its document.
	UnsavedChangedOutside UnsavedCause = "changed_outside"
	// UnsavedNotWritten is a draft whose publication failed.
	UnsavedNotWritten UnsavedCause = "not_written"
	// UnsavedAgentEditHeld is a draft holding an agent edit, which only the
	// reviewed publication door may publish.
	UnsavedAgentEditHeld UnsavedCause = "agent_edit_held"
)

// errAgentEditHeld stops a launch save at a draft holding unpublished agent text.
var errAgentEditHeld = errors.New("document holds an unpublished agent edit")

// errChangedOutside stops a launch save at a draft whose file changed outside it.
var errChangedOutside = errors.New("file changed outside its open document")

// PinnedRevision is a document revision a program's native operation captured.
type PinnedRevision struct {
	DocumentID string
	Revision   int64
}

// launchSave is a launch's rule for one draft: it publishes only the person's
// own text, and leaves a revision the launching program has pinned to the
// operation that pinned it.
type launchSave struct {
	pinned map[PinnedRevision]bool
}

// UnsavedDocument is a draft a launch save could not publish.
type UnsavedDocument struct {
	RootID, Path string
	Cause        UnsavedCause
}

// PublicationFailure is a draft the document still holds because its save failed.
type PublicationFailure struct {
	RootID, Path string
	Err          error
}

func (e *PublicationFailure) Error() string { return "save " + e.Path + ": " + e.Err.Error() }

func (e *PublicationFailure) Unwrap() error { return e.Err }

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
		return s.saveForAgent(ctx, p, state.ID, call, nil)
	})
}

// SaveForAgentLaunch saves the person's drafts on the branch before a process
// reads the tree from disk. A draft it cannot publish is returned, not
// refused: the launch overwrites nothing. A pinned revision is skipped: saving
// it would move the document under the operation that captured it.
func (s *Service) SaveForAgentLaunch(ctx context.Context, projectID string, branch sourcebranch.ID, call AgentCall, pinned []PinnedRevision) ([]UnsavedDocument, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	p, err := s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return nil, err
	}
	launch := &launchSave{pinned: make(map[PinnedRevision]bool, len(pinned))}
	for _, pin := range pinned {
		launch.pinned[pin] = true
	}
	var unsaved []UnsavedDocument
	err = s.VisitUnsavedDocuments(ctx, p.ID, branch, "", "", true, func(state DocumentPathState) error {
		cause, err := launchSaveCause(ctx, s.saveForAgent(ctx, p, state.ID, call, launch))
		if err != nil {
			return err
		}
		if cause != "" {
			unsaved = append(unsaved, UnsavedDocument{RootID: state.RootID, Path: state.Path, Cause: cause})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return unsaved, nil
}

// launchSaveCause separates a draft left unsaved from a save that could not run.
func launchSaveCause(ctx context.Context, err error) (UnsavedCause, error) {
	var conflict *ExecutionConflict
	var failed *PublicationFailure
	switch {
	case err == nil:
		return "", nil
	case ctx.Err() != nil:
		return "", err
	case errors.Is(err, errChangedOutside), errors.As(err, &conflict):
		return UnsavedChangedOutside, nil
	case errors.As(err, &failed):
		return UnsavedNotWritten, nil
	case errors.Is(err, errAgentEditHeld):
		return UnsavedAgentEditHeld, nil
	}
	return "", err
}

// saveForAgent publishes one draft for an agent's call. A mutation at the
// path passes no launch rule and publishes what the document holds.
func (s *Service) saveForAgent(ctx context.Context, p *project.Project, documentID string, call AgentCall, launch *launchSave) error {
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
	if launch != nil {
		if launch.pinned[PinnedRevision{DocumentID: current.ID, Revision: current.Revision}] {
			return nil
		}
		held, err := s.holdsUnpublishedAgentText(ctx, current)
		if err != nil {
			return err
		}
		if held {
			return errAgentEditHeld
		}
		// A launch needs the fact, not the hashes a refusal reports.
		if current.Diverged {
			return errChangedOutside
		}
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
		if launch != nil {
			return errChangedOutside
		}
		return s.fileChangedOutside(p, current)
	}
	if err != nil {
		return &PublicationFailure{RootID: current.RootID, Path: current.Path, Err: err}
	}
	if saved != nil {
		s.agentReads.replaceRevision(p.ID, current.ID, current.Revision, saved.Revision)
	}
	return nil
}

// holdsUnpublishedAgentText reports agent text accepted since the document's
// last publication.
func (s *Service) holdsUnpublishedAgentText(ctx context.Context, d *Document) (bool, error) {
	if d.HeldAgentVersionID != "" {
		return true, nil
	}
	var agentChanges int
	err := s.store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM editor_document_changes c JOIN editor_replica_heads h ON h.document_id=c.document_id
 WHERE c.document_id=? AND c.epoch=h.epoch AND c.revision>h.published_revision AND c.actor_kind='agent' AND COALESCE(c.reverted_by,'')=''`,
		d.ID,
	).Scan(&agentChanges)
	return agentChanges > 0, err
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
