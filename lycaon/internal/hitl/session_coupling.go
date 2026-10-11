package hitl

import "context"

func (m *SessionCoupling) SetCheckpointWaitObserver(observe func(ctx context.Context, sessionID string) (end func())) {
	if m != nil {
		m.checkpointWait = observe
	}
}

func (m *SessionCoupling) ObserveCheckpointWait(ctx context.Context, checkpointID string) func() {
	if m == nil || m.checkpointWait == nil {
		return func() {}
	}
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil || row == nil {
		return func() {}
	}
	return m.checkpointWait(ctx, row.SessionID)
}

func (m *SessionCoupling) SetSessionAdmission(admit func(ctx context.Context, sessionID string, fn func() error) error) {
	if m != nil {
		m.admission = admit
	}
}

type SessionCoupling struct {
	store interface {
		Get(context.Context, string) (*StoredCheckpoint, error)
	}
	admission      func(context.Context, string, func() error) error
	checkpointWait func(context.Context, string) func()
}
