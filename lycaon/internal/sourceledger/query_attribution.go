package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

type AttributionInterval struct {
	StartLine, EndLine int
	Origin             api.SourceChangeOrigin
	ActorLabel         string
	PersonID           string
	SessionID          string
	JobID              string
	Turn               int
	ToolCallID         string
	TS                 time.Time
	EffectID           string
}

type AttributionResult struct {
	FileID     string
	HeadSHA256 string
	Intervals  []AttributionInterval
}

func (s *History) QueryAttribution(
	ctx context.Context,
	projectID string, branch sourcebranch.ID, rootID, path string,
) (AttributionResult, error) {
	out := AttributionResult{Intervals: []AttributionInterval{}}
	if s == nil || projectID == "" || rootID == "" || path == "" {
		return out, nil
	}
	head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
		ProjectID: projectID, BranchID: branch.String(), RootID: rootID, Path: path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.FileID, out.HeadSHA256 = head.FileID, head.ContentSha256
	attrs, err := s.queries.ListSourceLineAttrForFile(ctx, db.ListSourceLineAttrForFileParams{
		ProjectID: projectID, BranchID: branch.String(), FileID: head.FileID,
	})
	if err != nil {
		return out, err
	}
	operations := make(map[string]db.GetSourceOperationRow)
	for _, attr := range attrs {
		effect, effectErr := s.queries.GetSourceEffect(ctx, attr.EffectID)
		if errors.Is(effectErr, sql.ErrNoRows) {
			continue
		}
		if effectErr != nil {
			return out, effectErr
		}
		operation, ok := operations[effect.OperationID]
		if !ok {
			operation, effectErr = s.queries.GetSourceOperation(ctx, effect.OperationID)
			if effectErr != nil {
				return out, effectErr
			}
			operations[effect.OperationID] = operation
		}
		ts, _ := time.Parse(time.RFC3339Nano, effect.CreatedTs)
		out.Intervals = append(out.Intervals, AttributionInterval{
			StartLine: int(attr.StartLine), EndLine: int(attr.EndLine),
			Origin:     api.SourceChangeOrigin(operation.Origin),
			ActorLabel: operation.ActorLabel,
			PersonID:   operation.PersonID.String,
			SessionID:  operation.SessionID, JobID: operation.JobID, Turn: int(operation.Turn),
			ToolCallID: operation.ToolCallID, TS: ts, EffectID: effect.ID,
		})
	}
	return s.comparisons.savedTextAttribution(ctx, projectID, head.VersionID, out)
}
