package editordoc

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// VisitAgentPublications offers pure agent drafts to the host write authority.
// The save callback rechecks the exact accepted revision after admission.
func (s *Service) VisitAgentPublications(ctx context.Context, projectID string, branch sourcebranch.ID, sessionID string, turn int, toolCallID string, visit func(DocumentPathState, func(context.Context) (*Document, func(context.Context) (*Document, error), error)) error) error {
	if s == nil || s.store == nil {
		return nil
	}
	p, err := s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return err
	}
	return s.VisitUnsavedDocuments(ctx, p.ID, branch, "", "", true, func(state DocumentPathState) error {
		ok, err := s.isPureAgentDraft(ctx, state.ID, sessionID)
		if err != nil || !ok {
			return err
		}
		return visit(state, func(ctx context.Context) (*Document, func(context.Context) (*Document, error), error) {
			doc, err := s.store.get(ctx, `SELECT `+documentColumns+` FROM editor_documents WHERE id=? AND project_id=? AND revision=? AND length(CAST(draft AS BLOB))=? AND length(CAST(base_content AS BLOB))=?`, state.ID, p.ID, state.Revision, state.DraftBytes, state.BaseBytes)
			if err != nil {
				return nil, nil, err
			}
			return doc, func(ctx context.Context) (*Document, error) {
				return s.publishOneAgentEdit(ctx, p, state.ID, state.Revision, sessionID, turn, toolCallID)
			}, nil
		})
	})
}

func (s *Service) publishOneAgentEdit(ctx context.Context, p *project.Project, docID string, expected int64, sessionID string, turn int, toolCallID string) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(docID))
	defer unlock()
	current, err := s.checked(ctx, docID, p.ID)
	if err != nil {
		return nil, err
	}
	if current.Revision != expected {
		return nil, ErrRevisionConflict
	}
	if !current.Dirty || current.Diverged || current.BranchID != p.BranchForRoot(current.RootID) {
		return nil, ErrRevisionConflict
	}
	ok, err := s.isPureAgentDraft(ctx, current.ID, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrRevisionConflict
	}
	saved, err := s.saveLocked(ctx, p, current, uuid.NewString(), saveActor{
		Origin:     api.SourceChangeOriginAgent,
		SessionID:  strings.TrimSpace(sessionID),
		Turn:       turn,
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   "command",
	})
	if err != nil {
		return nil, err
	}
	if saved != nil {
		s.agentReads.replaceRevision(p.ID, current.ID, current.Revision, saved.Revision)
	}
	return saved, nil
}

func (s *Service) isPureAgentDraft(ctx context.Context, documentID, sessionID string) (bool, error) {
	var dirty, diverged bool
	var epoch, publishedRevision int64
	var held string
	if err := s.store.db.QueryRowContext(ctx, `SELECT d.dirty,d.diverged,h.epoch,h.published_revision,d.held_agent_version_id
 FROM editor_documents d JOIN editor_replica_heads h ON h.document_id=d.id WHERE d.id=?`, documentID).Scan(&dirty, &diverged, &epoch, &publishedRevision, &held); err != nil {
		return false, err
	}
	if !dirty || diverged {
		return false, nil
	}
	var nonAgentChanges int
	if err := s.store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM editor_document_changes WHERE document_id=? AND epoch=? AND revision>? AND actor_kind!='agent' AND COALESCE(reverted_by,'')=''`,
		documentID, epoch, publishedRevision,
	).Scan(&nonAgentChanges); err != nil {
		return false, err
	}
	if nonAgentChanges > 0 {
		return false, nil
	}
	var nonAgentContribs int
	if err := s.store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM source_text_contributions WHERE document_id=? AND epoch=? AND revision>? AND (origin!='agent' OR session_id!=?)`,
		documentID, epoch, publishedRevision, sessionID,
	).Scan(&nonAgentContribs); err != nil {
		return false, err
	}
	if nonAgentContribs > 0 {
		return false, nil
	}
	if held != "" {
		return true, nil
	}
	var agentChanges int
	if err := s.store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM editor_document_changes WHERE document_id=? AND epoch=? AND revision>? AND actor_kind='agent' AND COALESCE(reverted_by,'')=''`,
		documentID, epoch, publishedRevision,
	).Scan(&agentChanges); err != nil {
		return false, err
	}
	return agentChanges > 0, nil
}
