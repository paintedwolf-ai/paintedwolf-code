package profiles

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectedSurfaceDoesNotDeliverPostureChanges(t *testing.T) {
	var posture, external int
	service := &Service{
		postureObserver:       func(context.Context, string, gate.Posture) { posture++ },
		externalObserver:      func(context.Context, string, gate.Posture, bool) { external++ },
		commandExternalHidden: func() bool { return true },
	}
	sess := &api.Session{ID: "chat"}
	projected := service.promptSurface(t.Context(), sess, nil, hostresources.Snapshot{}, nil, false, false)
	if posture != 0 || external != 0 {
		t.Fatal("projection delivered a posture observation")
	}
	normal := service.PromptSurface(t.Context(), sess, nil, hostresources.Snapshot{}, nil, false)
	if projected.Fingerprint != normal.Fingerprint || !projected.CommandExternalHidden {
		t.Fatal("projection differs from normal prompt surface")
	}
	if posture != 1 || external != 1 {
		t.Fatal("normal turn lost posture observations")
	}
}
