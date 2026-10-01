package worker

// RunnableNotifier announces newly claimable durable queue state.
type RunnableNotifier interface {
	NotifyRunnable()
}

// RunnableWakeSource exposes process-local runnable notifications to a claimer.
type RunnableWakeSource interface {
	RunnableWake() <-chan struct{}
}

type runnableSignal struct {
	wake chan struct{}
}

func newRunnableSignal() *runnableSignal {
	return &runnableSignal{wake: make(chan struct{}, 1)}
}

func (s *runnableSignal) NotifyRunnable() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *runnableSignal) RunnableWake() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.wake
}
