package promptloop

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

type activityLease struct {
	mu           sync.Mutex
	event        api.ActivityEvent
	publish      func(api.ActivityEvent)
	lastProgress time.Time
	finished     bool
}

// beginActivity opens one host-observed activity lease.
func (l *turnProjection) beginActivity(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	kind api.ActivityKind,
	toolName string,
	toolCallID string,
) *activityLease {
	if l == nil || l.Deps.Events == nil || sess == nil || strings.TrimSpace(sessionID) == "" {
		return &activityLease{}
	}
	event := api.ActivityEvent{
		ActivityID: uuid.NewString(),
		SessionID:  strings.TrimSpace(sessionID),
		Kind:       kind,
		Status:     api.ActivityStatusActive,
		StartedAt:  time.Now().UTC(),
		ToolName:   strings.TrimSpace(toolName),
		ToolCallID: strings.TrimSpace(toolCallID),
	}
	l.Deps.Events.PublishActivity(ctx, sessionProjectKey(sess), event.SessionID, event)
	return &activityLease{event: event, publish: func(update api.ActivityEvent) {
		l.Deps.Events.PublishActivity(context.WithoutCancel(ctx), sessionProjectKey(sess), event.SessionID, update)
	}}
}

func (a *activityLease) report(progress api.ToolProgress) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.publish == nil {
		return
	}
	phaseChanged := a.event.Progress == nil || a.event.Progress.Phase != progress.Phase
	a.event.Progress = &progress
	if phaseChanged || time.Since(a.lastProgress) >= time.Second {
		a.lastProgress = time.Now()
		a.publish(a.event)
	}
}

func (a *activityLease) finish() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.publish == nil {
		return
	}
	a.finished = true
	a.event.Status = api.ActivityStatusDone
	a.publish(a.event)
}

func coordinatorLLMLoopProgress(profileID, surfaceID, activityLabel string, hostTurn bool, iterIndex, maxIter int, proseFinish bool) *api.CoordinatorLoopProgress {
	if !guard.IsCoordinatorProfile(profileID) {
		return nil
	}
	loop := &api.CoordinatorLoopProgress{
		Surface:           surfaceID,
		ActivityLabel:     strings.TrimSpace(activityLabel),
		Guarded:           true,
		ProvisionalHidden: surface.SurfaceDeliversReport(surfaceID),
		Iteration:         iterIndex + 1,
		MaxIterations:     maxIter,
		ProseFinish:       proseFinish,
	}
	if hostTurn {
		loop.HostTurn = true
	}
	return loop
}

func (l *promptContext) coordinatorSurfaceActivityLabel(surfaceID string) string {
	if l == nil || l.Deps.CoordinatorSurfaceActivityLabel == nil {
		return ""
	}
	return l.Deps.CoordinatorSurfaceActivityLabel(surfaceID)
}
