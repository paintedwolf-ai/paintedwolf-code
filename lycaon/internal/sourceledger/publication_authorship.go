package sourceledger

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func recordTextState(ctx context.Context, q *db.Queries, versionID string, state *TextState) error {
	if versionID == "" || state == nil {
		return nil
	}
	encoded, err := json.Marshal(state.Spans)
	if err != nil {
		return err
	}
	return q.InsertSourceVersionTextState(ctx, db.InsertSourceVersionTextStateParams{
		Revision: state.Revision, VersionID: versionID, DocumentID: state.DocumentID, Epoch: state.Epoch, SpansJson: string(encoded),
	})
}

func recordPublicationAuthorship(ctx context.Context, q *db.Queries, beforeVersionID, afterVersionID, effectID string, in RecordInput) (bool, error) {
	if err := recordTextState(ctx, q, beforeVersionID, in.TextBefore); err != nil {
		return false, err
	}
	if err := recordTextState(ctx, q, afterVersionID, in.TextAfter); err != nil {
		return false, err
	}
	if in.TextBefore == nil || in.TextAfter == nil || in.TextBefore.DocumentID != in.TextAfter.DocumentID || in.TextBefore.Epoch != in.TextAfter.Epoch {
		return in.Origin == api.SourceChangeOriginAgent, nil
	}
	inserted := indexTextIdentities(changedTextSpans(in.TextAfter.Spans, in.TextBefore.Spans))
	deleted := indexTextIdentities(changedTextSpans(in.TextBefore.Spans, in.TextAfter.Spans))
	ranges, err := contributionRanges(ContributionSelection{Inserted: changedTextSpans(in.TextAfter.Spans, in.TextBefore.Spans), Deleted: changedTextSpans(in.TextBefore.Spans, in.TextAfter.Spans)})
	if err != nil {
		return false, err
	}
	contributions, err := q.ListSelectedTextContributions(ctx, db.ListSelectedTextContributionsParams{DocumentID: in.TextAfter.DocumentID, Epoch: in.TextAfter.Epoch, ThroughRevision: in.TextAfter.Revision, RangesJson: ranges})
	if err != nil {
		return false, err
	}
	hasContributors, hasAgent := false, false
	for _, contribution := range contributions {
		var additions, removals []TextIdentityRange
		if err := json.Unmarshal([]byte(contribution.InsertedJson), &additions); err != nil {
			return false, err
		}
		if err := json.Unmarshal([]byte(contribution.DeletedJson), &removals); err != nil {
			return false, err
		}
		if inserted.intersects(additions) || deleted.intersects(removals) {
			hasContributors = true
			hasAgent = hasAgent || contribution.Origin == string(api.SourceChangeOriginAgent)
			if err := q.InsertSourceEffectContribution(ctx, db.InsertSourceEffectContributionParams{EffectID: effectID, ContributionID: contribution.ID}); err != nil {
				return false, err
			}
		}
	}
	return hasAgent || (!hasContributors && in.Origin == api.SourceChangeOriginAgent), nil
}
