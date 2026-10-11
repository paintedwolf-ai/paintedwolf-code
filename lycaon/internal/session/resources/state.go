package resources

import (
	"context"

	"github.com/lycaon/lycaon/internal/progress"
)

type Forgetter interface{ Forget(string) }
type ContextForgetter interface{ Forget(context.Context, string) }
type Capture interface{ Reset(string) }
type Streams interface{ Finish(context.Context, string) }
type Batch interface{ BeginTurn(string) }
type History interface{ ForgetCalibration(string) }

// TurnState invalidates captures, ledgers, and derived facts from a released run.
type TurnState struct {
	Capture         Capture
	Streams         Streams
	ProgressClosure Forgetter
	Closeouts       Forgetter
	Settlement      Forgetter
	Spend           ContextForgetter
	Batch           Batch
	Promotion       Forgetter
	History         History
	Gate            Forgetter
	PolicyIndex     Forgetter
	Protection      Forgetter
}

func (s *TurnState) Forget(ctx context.Context, id string) {
	if s.Capture != nil {
		s.Capture.Reset(id)
	}
	if s.Streams != nil {
		s.Streams.Finish(ctx, id)
	}
	if s.ProgressClosure != nil {
		s.ProgressClosure.Forget(id)
	}
	if s.Closeouts != nil {
		s.Closeouts.Forget(id)
	}
	if s.Settlement != nil {
		s.Settlement.Forget(id)
	}
	if s.Spend != nil {
		s.Spend.Forget(ctx, id)
	}
	if s.Batch != nil {
		s.Batch.BeginTurn(id)
	}
	if s.Promotion != nil {
		s.Promotion.Forget(id)
	}
	if s.History != nil {
		s.History.ForgetCalibration(id)
	}
	if s.Gate != nil {
		s.Gate.Forget(id)
	}
	if s.PolicyIndex != nil {
		s.PolicyIndex.Forget(id)
	}
	progress.ForgetClock(id)
	if s.Protection != nil {
		s.Protection.Forget(id)
	}
}
