package events

import (
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// sessionLifecycle holds the session event fields whose changes are edges.
type sessionLifecycle struct {
	status        api.SessionStatus
	promptPending bool
}

// publishSession coalesces updates within one lifecycle state and delivers
// edges at once: a lifecycle change, an idle disposition, a host error, or a
// session's first event. A held edge would trail the transcript rows that
// deliver immediately; a replaced one would hide how a turn began or ended.
func (h *MemoryHub) publishSession(env api.EventEnvelope, key PublishKey, window time.Duration) {
	var event api.SessionEvent
	decoded := json.Unmarshal(env.Data, &event) == nil
	lifecycle := sessionLifecycle{status: event.Status, promptPending: event.PromptPending}
	h.mu.Lock()
	defer h.mu.Unlock()
	dkey := debounceKey(env.Topic, key)
	last, known := h.lifecycles[dkey]
	edge := !decoded || !known || last != lifecycle || event.IdleDisposition != "" || event.HostError != nil
	if !edge && window > 0 {
		h.debounceLocked(dkey, debouncedEntry{env: env, lifecycle: &lifecycle}, window)
		return
	}
	// An older pending update is superseded; a newer one still follows.
	if pending, ok := h.pending[dkey]; ok && (pending.env.EntityRevision == 0 ||
		env.EntityRevision == 0 || pending.env.EntityRevision <= env.EntityRevision) {
		delete(h.pending, dkey)
		if timer, ok := h.timers[dkey]; ok {
			timer.Stop()
			delete(h.timers, dkey)
		}
	}
	h.deliverLocked(env)
	if decoded {
		h.lifecycles[dkey] = lifecycle
	}
}
