package actionoutcome

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/pkg/api"
)

type TurnRecord struct {
	TurnID       string                     `json:"turn_id"`
	SessionID    string                     `json:"session_id"`
	AdmittedAt   time.Time                  `json:"admitted_at"`
	DispatchedAt time.Time                  `json:"dispatched_at,omitzero"`
	SettledAt    time.Time                  `json:"settled_at,omitzero"`
	Outcome      api.TurnOutcomeKind        `json:"outcome,omitempty"`
	Detail       promptresult.Outcome       `json:"detail,omitempty"`
	FailureCode  api.NoticeCode             `json:"failure_code,omitempty"`
	CancelledBy  api.TurnCancellationSource `json:"cancelled_by,omitempty"`
}

type turnContextKey struct{}
type Turn struct {
	path    *Path
	id      string
	history *History[TurnRecord]
}

func BeginTurn(ctx context.Context, history *History[TurnRecord], sessionID, turnID string) (context.Context, *Turn) {
	history.Open(turnID, TurnRecord{TurnID: turnID, SessionID: sessionID, AdmittedAt: time.Now().UTC()})
	var path *Path
	if ticket, _ := ctx.Value(ticketKey{}).(*Ticket); ticket != nil {
		ticket.Record(api.WakeStateDelivered, "", turnID, "")
		path = ticket.path
		ctx = WithTicket(ctx, nil)
	}
	t := &Turn{path: path, id: turnID, history: history}
	return context.WithValue(ctx, turnContextKey{}, t), t
}

func Dispatch(ctx context.Context) {
	t, _ := ctx.Value(turnContextKey{}).(*Turn)
	if t == nil {
		return
	}
	t.history.Change(t.id, func(r TurnRecord) (TurnRecord, bool) {
		if r.DispatchedAt.IsZero() {
			r.DispatchedAt = time.Now().UTC()
		}
		return r, false
	})
	t.path.Resolve(Result{State: api.ContinuationStateContinued, TurnID: t.id})
}

func (t *Turn) Finish(result *promptresult.Result, err error, cancelledBy api.TurnCancellationSource) {
	t.history.Change(t.id, func(r TurnRecord) (TurnRecord, bool) {
		r.SettledAt = time.Now().UTC()
		r.Outcome = api.TurnOutcomeKindCompleted
		if result != nil {
			r.Detail = result.Outcome
		}
		switch {
		case errors.Is(err, context.Canceled):
			r.Outcome = api.TurnOutcomeKindCancelled
			r.CancelledBy = cancelledBy
		case err != nil:
			r.Outcome = api.TurnOutcomeKindFailed
			r.FailureCode = PromptFailureCode(err)
		}
		if r.DispatchedAt.IsZero() {
			result := WakeResult(api.WakeReasonTurnSettled)
			if r.Outcome == api.TurnOutcomeKindFailed {
				result = Result{State: api.ContinuationStateFailed, FailureCode: string(r.FailureCode)}
			}
			result.TurnID = r.TurnID
			t.path.Resolve(result)
		}
		return r, true
	})
}

func PromptFailureCode(err error) api.NoticeCode {
	if code, ok := noticeerr.CodeOf(err); ok {
		return code
	}
	if httpclient.Unreachable(err) {
		return api.NoticeCodeProviderUnreachable
	}
	return api.NoticeCodePromptFailed
}
