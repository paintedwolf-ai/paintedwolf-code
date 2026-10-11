package resources

import "context"

type Cancellation interface{ Cancel(string) }
type SessionCancellation interface{ CancelSession(string) }
type Coordinator interface{ ForgetSession(context.Context, string) }
type SessionForgetter interface{ ForgetSession(string) }

// Work cancels live execution before dropping the state it can still access.
type Work struct {
	Execution   Cancellation
	Curation    Cancellation
	History     SessionCancellation
	Research    SessionCancellation
	Coordinator Coordinator
	Workflow    SessionForgetter
}

func (w *Work) Cancel(ctx context.Context, id string) {
	if w.Execution != nil {
		w.Execution.Cancel(id)
	}
	if w.Curation != nil {
		w.Curation.Cancel(id)
	}
	if w.History != nil {
		w.History.CancelSession(id)
	}
	if w.Research != nil {
		w.Research.CancelSession(id)
	}
	if w.Coordinator != nil {
		w.Coordinator.ForgetSession(ctx, id)
	}
	if w.Workflow != nil {
		w.Workflow.ForgetSession(id)
	}
}
