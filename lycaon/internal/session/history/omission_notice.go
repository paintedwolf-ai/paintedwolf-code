package history

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

const omittedOrdinalsPrefix = "[host:omitted history ordinals "

func (m *Service) effectiveRecallAvailable(ctx context.Context, sess *api.Session) bool {
	if req := toolreach.FromContext(ctx); req.Known() {
		return req.Status("recall") == toolreach.Callable
	}
	return m != nil && m.recallAvailable != nil && m.recallAvailable(ctx, sess)
}

func recallReach(ctx context.Context, recallAvailable bool) toolreach.Request {
	if !recallAvailable {
		return toolreach.Offered(nil)
	}
	if req := toolreach.FromContext(ctx); req.Known() {
		return req
	}
	return toolreach.Offered(nil)
}

func parseOmittedOrdinalRange(content string) (int64, int64, bool) {
	if !strings.HasPrefix(content, omittedOrdinalsPrefix) {
		return 0, 0, false
	}
	rest := content[len(omittedOrdinalsPrefix):]
	semi := strings.IndexByte(rest, ';')
	if semi < 0 {
		return 0, 0, false
	}
	fromStr, throughStr, ok := strings.Cut(rest[:semi], " through ")
	if !ok {
		return 0, 0, false
	}
	from, err1 := strconv.ParseInt(strings.TrimSpace(fromStr), 10, 64)
	through, err2 := strconv.ParseInt(strings.TrimSpace(throughStr), 10, 64)
	if err1 != nil || err2 != nil || from <= 0 || through < from {
		return 0, 0, false
	}
	return from, through, true
}

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
	return omissionNoticeForRecall(ctx, recallAvailable != nil && recallAvailable(ctx, sess), id, from, through)
}

func omissionNoticeForRecall(ctx context.Context, recall bool, id string, from, through int64) (*api.Message, error) {
	text, err := guidance.RenderGuidanceWithReach(ctx, recallReach(ctx, recall), guidance.CompactionContinuationRecordRef, map[string]any{
		"level": "references", "through_ord": through, "omitted_from_ord": from, "omitted_through_ord": through,
	})
	if err != nil {
		return nil, err
	}
	return &api.Message{ID: id, Role: api.MessageRoleSystem, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContextPinned: true, Content: text}, nil
}
