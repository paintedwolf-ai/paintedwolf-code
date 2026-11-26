package actionoutcome

import (
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

type Notices struct {
	mu      sync.Mutex
	order   uint64
	current api.ContinuationNotice
	publish func(api.ContinuationNotice)
}

func (n *Notices) Snapshot() *api.ContinuationNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.current.Revision == 0 {
		return nil
	}
	value := n.current
	return &value
}

func (n *Notices) Observe(order uint64, a api.ActionContinuation, render func(string, api.ContinuationAction) *api.SessionHostError, publish func(api.ContinuationNotice)) {
	var hostError *api.SessionHostError
	switch a.State {
	case api.ContinuationStateFailed:
		if a.TurnID != "" {
			return
		}
		hostError = render(a.FailureCode, a.Action)
	case api.ContinuationStateNotContinued:
		if a.Reason == api.ContinuationReasonUserInterrupted {
			return
		}
		hostError = render(string(a.Reason), a.Action)
	case api.ContinuationStateContinued:
	default:
		return
	}
	n.mu.Lock()
	if order <= n.order {
		n.mu.Unlock()
		return
	}
	n.order = order
	if hostError == nil && n.current.ContinuationID == "" {
		n.mu.Unlock()
		return
	}
	value := api.ContinuationNotice{SessionID: a.SessionID, Revision: n.current.Revision + 1, HostError: hostError}
	if hostError != nil {
		value.ContinuationID = a.ContinuationID
	}
	n.current = value
	n.publish = publish
	n.mu.Unlock()
	publish(value)
}

func (n *Notices) Recover(id string) {
	if id == "" {
		return
	}
	n.mu.Lock()
	if n.current.ContinuationID != id {
		n.mu.Unlock()
		return
	}
	value := api.ContinuationNotice{SessionID: n.current.SessionID, Revision: n.current.Revision + 1}
	n.current = value
	publish := n.publish
	n.publish = nil
	n.mu.Unlock()
	publish(value)
}
