package recall

import (
	"math"
	"strings"
)

func validateHistoryRequest(req Request) error {
	h := req.History
	switch {
	case h.Format != "" && h.Format != "text" && h.Format != "record":
		return &ErrHistoryCoordinates{Field: "history.format", Reason: "unsupported body format"}
	case h.Format == "record" && h.MessageID == "":
		return &ErrHistoryCoordinates{Field: "history.message_id", Reason: "record format needs a message ID"}
	case h.BodyRunes < 0 || h.BodyRunes > historyBodyRunes:
		return &ErrHistoryCoordinates{Field: "history.body_runes", Reason: "body window is outside the allowed range"}
	case h.Offset < 0 || h.Offset == math.MaxInt:
		return &ErrHistoryCoordinates{Field: "history.offset", Reason: "body offset is outside the allowed range"}
	case len([]rune(h.Search)) > 256 || len(strings.Fields(h.Search)) > 8:
		return &ErrHistoryCoordinates{Field: "history.search", Reason: "history search needs at most eight terms and 256 characters"}
	case h.MessageID != "" && strings.TrimSpace(h.Search) != "":
		return &ErrHistoryCoordinates{Field: "history.search", Reason: "body recovery cannot also search the index"}
	case h.ThroughOrd < 0 || h.ThroughOrd == 0 && h.MessageID == "":
		return &ErrHistoryCoordinates{Field: "history.through_ord", Reason: "index paging needs a positive upper ordinal"}
	case h.AfterOrd < 0 || h.ThroughOrd > 0 && h.AfterOrd >= h.ThroughOrd:
		return &ErrHistoryCoordinates{Field: "history.after_ord", Reason: "start ordinal must be nonnegative and below the upper ordinal"}
	case h.SourceSeq < 0:
		return &ErrHistoryCoordinates{Field: "history.source_seq", Reason: "source sequence must be nonnegative"}
	case strings.TrimSpace(req.Caller.SessionID) == "":
		return &ErrHistoryCoordinates{Reason: "caller session is unavailable"}
	case req.Widen != WidenDefault || strings.TrimSpace(req.Query) != "":
		return &ErrHistoryCoordinates{Field: "history", Reason: "history cannot be combined with query or widen"}
	}
	return nil
}
