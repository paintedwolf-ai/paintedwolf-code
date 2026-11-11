package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// tailOmissionNotice names rows between coverage and the canonical tail that hard
// limits left out. The opener is retained separately, so it does not count.
func tailOmissionNotice(ctx context.Context, recallAvailable func(context.Context, *api.Session) bool, sess *api.Session, covered, oldest int64, tail []api.Message) (*api.Message, error) {
	through := oldest - 1
	if len(tail) > 0 && tail[0].Ord == through {
		through--
	}
	if covered+1 > through {
		return nil, nil
	}
	return omissionNotice(ctx, recallAvailable, sess, historyCoverageNoticeID, covered+1, through)
}

// omissionNotice is reference-only: it establishes no summary coverage.
func omissionNotice(ctx context.Context, recallAvailable func(context.Context, *api.Session) bool, sess *api.Session, id string, from, through int64) (*api.Message, error) {
	text, err := guidance.RenderGuidance(ctx, guidance.CompactionContinuationRecordRef, map[string]any{
		"level": "references", "through_ord": through, "omitted_from_ord": from, "omitted_through_ord": through,
		"recall_available": recallAvailable != nil && recallAvailable(ctx, sess),
	})
	if err != nil {
		return nil, err
	}
	return &api.Message{ID: id, Role: api.MessageRoleSystem, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContextPinned: true, Content: text}, nil
}
