package sourceledger

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
)

// maxEffectsByID bounds one named-effect read.
const maxEffectsByID = 256

// EffectsByID reads the named effects of one project, oldest first, with
// their authors. Ids from another project are not returned.
func (s *Walk) EffectsByID(ctx context.Context, projectID string, ids []string) ([]Effect, error) {
	if len(ids) > maxEffectsByID {
		return nil, fmt.Errorf("at most %d effects may be read by id", maxEffectsByID)
	}
	rows, err := s.queries.ListSourceEffectsByIDs(ctx, db.ListSourceEffectsByIDsParams{ProjectID: projectID, EffectIds: ids})
	if err != nil {
		return nil, err
	}
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out, s.hydrateEffectAuthors(ctx, projectID, out)
}
