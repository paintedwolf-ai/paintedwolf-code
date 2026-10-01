package events

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func cloneActivity(event api.ActivityEvent) api.ActivityEvent {
	event.WaitTriggers = slices.Clone(event.WaitTriggers)
	if event.Progress != nil {
		progress := *event.Progress
		event.Progress = &progress
	}
	return event
}

func (p *Publisher) observeActivity(event api.ActivityEvent) {
	if event.SessionID == "" || strings.TrimSpace(event.ActivityID) == "" {
		return
	}
	p.activitiesMu.Lock()
	defer p.activitiesMu.Unlock()
	if event.Status == api.ActivityStatusActive {
		if p.activities == nil {
			p.activities = make(map[string]map[string]api.ActivityEvent)
		}
		if p.activities[event.SessionID] == nil {
			p.activities[event.SessionID] = make(map[string]api.ActivityEvent)
		}
		p.activities[event.SessionID][event.ActivityID] = cloneActivity(event)
	} else {
		delete(p.activities[event.SessionID], event.ActivityID)
		if len(p.activities[event.SessionID]) == 0 {
			delete(p.activities, event.SessionID)
		}
	}
}

// SessionActivities replaces missed activity edges at a bootstrap boundary.
func (p *Publisher) SessionActivities(sessionID string) []api.ActivityEvent {
	if p == nil {
		return nil
	}
	p.activitiesMu.Lock()
	defer p.activitiesMu.Unlock()
	active := p.activities[strings.TrimSpace(sessionID)]
	result := make([]api.ActivityEvent, 0, len(active))
	for _, event := range active {
		result = append(result, cloneActivity(event))
	}
	slices.SortFunc(result, func(a, b api.ActivityEvent) int {
		return strings.Compare(a.ActivityID, b.ActivityID)
	})
	return result
}
