package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// TextIdentityRange names characters independently of their current position.
type TextIdentityRange struct {
	Client uint32 `json:"client"`
	Start  uint32 `json:"start"`
	End    uint32 `json:"end"`
}

type TextContribution struct {
	ID, ProjectID, FileID, DocumentID string
	Epoch, Revision                   int64
	OperationID                       string
	// PersonID and ClientID name who typed user text and in which client.
	PersonID, ClientID   string
	Origin               api.SourceChangeOrigin
	SessionID            string
	Turn                 int
	ToolCallID, ToolName string
	JobID                string
	Inserted, Deleted    []TextIdentityRange
	CreatedAt            time.Time
}

func TextContributionID(documentID, operationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("source-contribution:"+documentID+":"+operationID)).String()
}

// RecordTextContributionTx commits authorship with the accepted document update.
func RecordTextContributionTx(ctx context.Context, tx *sql.Tx, contribution TextContribution) error {
	inserted, err := json.Marshal(contribution.Inserted)
	if err != nil {
		return err
	}
	deleted, err := json.Marshal(contribution.Deleted)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_text_contributions
 (id,project_id,file_id,document_id,epoch,revision,operation_id,origin,person_id,client_id,session_id,turn,tool_call_id,tool_name,job_id,inserted_json,deleted_json,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		TextContributionID(contribution.DocumentID, contribution.OperationID), contribution.ProjectID, contribution.FileID, contribution.DocumentID,
		contribution.Epoch, contribution.Revision, contribution.OperationID, contribution.Origin, db.NullString(contribution.PersonID), contribution.ClientID,
		contribution.SessionID, contribution.Turn, contribution.ToolCallID, contribution.ToolName, contribution.JobID, string(inserted), string(deleted),
		contribution.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	for kind, ranges := range map[string][]TextIdentityRange{"inserted": contribution.Inserted, "deleted": contribution.Deleted} {
		for _, span := range ranges {
			if span.End <= span.Start {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO source_text_identity_ranges
 (contribution_id,document_id,epoch,kind,client,clock_start,clock_end) VALUES(?,?,?,?,?,?,?)`,
				TextContributionID(contribution.DocumentID, contribution.OperationID), contribution.DocumentID, contribution.Epoch, kind, span.Client, span.Start, span.End); err != nil {
				return err
			}
		}
	}
	return nil
}

type ContributionSelection struct {
	ThroughRevision   int64
	Inserted, Deleted []TextSpan
}

type contributionRange struct {
	TextIdentityRange
	Kind string `json:"kind"`
}

func contributionRanges(selection ContributionSelection) (string, error) {
	ranges := make([]contributionRange, 0, len(selection.Inserted)+len(selection.Deleted))
	for kind, spans := range map[string][]TextSpan{"inserted": selection.Inserted, "deleted": selection.Deleted} {
		for _, span := range spans {
			if span.Length > 0 {
				ranges = append(ranges, contributionRange{TextIdentityRange: TextIdentityRange{Client: span.Client, Start: span.Clock, End: span.Clock + span.Length}, Kind: kind})
			}
		}
	}
	raw, err := json.Marshal(ranges)
	return string(raw), err
}

func (s *Store) DocumentContributions(ctx context.Context, documentID string, epoch int64, selection ContributionSelection) ([]TextContribution, error) {
	raw, err := contributionRanges(selection)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSelectedTextContributions(ctx, db.ListSelectedTextContributionsParams{DocumentID: documentID, Epoch: epoch, ThroughRevision: selection.ThroughRevision, RangesJson: raw})
	if err != nil {
		return nil, err
	}
	result := make([]TextContribution, 0, len(rows))
	for _, row := range rows {
		item := TextContribution{ID: row.ID, ProjectID: row.ProjectID, FileID: row.FileID, DocumentID: row.DocumentID, Epoch: row.Epoch, Revision: row.Revision, OperationID: row.OperationID, PersonID: db.StringFromNull(row.PersonID), ClientID: row.ClientID, Origin: api.SourceChangeOrigin(row.Origin), SessionID: row.SessionID, Turn: int(row.Turn), ToolCallID: row.ToolCallID, ToolName: row.ToolName, JobID: row.JobID}
		if err := json.Unmarshal([]byte(row.InsertedJson), &item.Inserted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(row.DeletedJson), &item.Deleted); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, row.CreatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}
