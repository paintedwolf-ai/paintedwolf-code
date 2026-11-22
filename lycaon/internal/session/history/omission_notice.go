package history

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

const omissionSignalPrefix = "history-omission:"

func effectiveRecallAvailable(ctx context.Context, recallAvailable func(context.Context, *api.Session) bool, sess *api.Session) bool {
	if req := toolreach.FromContext(ctx); req.Known() {
		return req.Status("recall") == toolreach.Callable
	}
	return recallAvailable != nil && recallAvailable(ctx, sess)
}

func formatOmissionSignal(from, through int64) string {
	if from <= 0 || through < from {
		return ""
	}
	return omissionSignalPrefix + strconv.FormatInt(from, 10) + ":" + strconv.FormatInt(through, 10)
}

func parseOmissionSignal(signalID string) (int64, int64, bool) {
	rest, ok := strings.CutPrefix(signalID, omissionSignalPrefix)
	if !ok {
		return 0, 0, false
	}
	fromStr, throughStr, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, 0, false
	}
	from, err1 := strconv.ParseInt(fromStr, 10, 64)
	through, err2 := strconv.ParseInt(throughStr, 10, 64)
	if err1 != nil || err2 != nil || from <= 0 || through < from {
		return 0, 0, false
	}
	return from, through, true
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
	return &api.Message{
		ID: id, Role: api.MessageRoleSystem, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		HostSignalID: formatOmissionSignal(from, through), ContextPinned: true, Content: text,
	}, nil
}

func omissionFallbackNoticeForRecall(ctx context.Context, recall bool, id string, coveredThroughOrd int64) (*api.Message, error) {
	vars := map[string]any{"level": "references"}
	if coveredThroughOrd > 0 {
		vars["through_ord"] = coveredThroughOrd
	}
	text, err := guidance.RenderGuidanceWithReach(ctx, recallReach(ctx, recall), guidance.CompactionContinuationRecordRef, vars)
	if err != nil {
		return nil, err
	}
	return &api.Message{
		ID: id, Role: api.MessageRoleSystem, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		ContextPinned: true, Content: text,
	}, nil
}
