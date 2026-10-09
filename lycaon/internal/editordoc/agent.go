package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

// AgentEdit applies changes anchored in a pinned revision to the current draft.
type AgentEdit struct {
	ProjectID        string
	DocumentID       string
	ExpectedRevision int64
	ReviewedRevision int64
	Content          string
	OperationID      string
	SessionID        string
	Turn             int
	ToolCallID       string
	ToolName         string
}

// An edit held against an externally changed file stays dirty and diverged.
type AgentEditResult struct {
	PublicationError error
	Document         *Document
	Saved            bool
	// HeldVersionID addresses the retained state holding an unsaved edit.
	HeldVersionID string
}

// OpenText opens editable source into the shared document, including unsaved
// edits. A deleted path is the document only while a draft is held for it.
func (s *Service) OpenText(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (*Document, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, nil
	}
	p, err := s.projectBranch(ctx, projectID, branch)
	if err != nil {
		return nil, false, err
	}
	snapshot, err := s.ResolveSourceSnapshot(ctx, p, projectsource.SourceReadRequest{Path: path, RootID: rootID}, AdmitEditable)
	if errors.Is(err, projectsource.ErrSourceNotFound) || errors.Is(err, projectsource.ErrSourceBinary) || errors.Is(err, projectsource.ErrSourceWriteTooLarge) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	d := snapshot.Document
	if d == nil || (d.Absent && !d.HoldsDraft()) {
		return nil, false, nil
	}
	return d, true, nil
}

// DirtyDocuments lists the documents holding unsaved edits for files that exist.
func (s *Service) DirtyDocuments(ctx context.Context, projectID string, branch sourcebranch.ID) ([]*Document, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	p, err := s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return nil, err
	}
	documents, err := s.store.queryDocuments(ctx, `SELECT `+documentColumns+` FROM editor_documents WHERE project_id=? AND dirty=1 AND absent=0 ORDER BY path,id`, p.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*Document, 0)
	for _, d := range documents {
		if d.Dirty && d.BranchID == p.BranchForRoot(d.RootID) {
			result = append(result, d)
		}
	}
	return result, nil
}

// ApplyAgentEdit accepts an anchored edit and journals its publication. A
// failed publication of an accepted edit is the result's PublicationError.
func (s *Service) ApplyAgentEdit(ctx context.Context, in AgentEdit) (*AgentEditResult, error) {
	results, err := s.ApplyAgentEdits(ctx, []AgentEdit{in})
	if err != nil {
		return nil, err
	}
	return results[0], nil
}

// retainAgentEditTx records accepted bytes that publication could not place on disk.
func (s *Service) retainAgentEditTx(ctx context.Context, tx *sql.Tx, d *Document, in AgentEdit) (string, error) {
	encoded, err := textfile.EncodeBounded(serializeEOL(d.Draft, d.EOL), d.Encoding, textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes))
	if err != nil {
		return "", err
	}
	return s.ledger.RecordHeldEditTx(ctx, tx, sourceledger.HeldEdit{
		BranchID:  d.BranchID,
		ProjectID: d.ProjectID, FileID: d.FileID, RootID: d.RootID, Path: d.Path,
		Content: encoded, SHA256: textfile.SHA256(encoded), Size: int64(len(encoded)),
		SessionID: strings.TrimSpace(in.SessionID), Turn: in.Turn, ToolCallID: strings.TrimSpace(in.ToolCallID), ToolName: strings.TrimSpace(in.ToolName), TS: d.UpdatedAt,
	})
}
