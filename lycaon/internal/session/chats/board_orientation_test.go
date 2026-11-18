package chats

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type orientationStore struct {
	Sessions
	sessions []*api.Session
}

func (s orientationStore) List(context.Context) ([]*api.Session, error) { return s.sessions, nil }

type orientationChanges []string

func (c *orientationChanges) InvalidateOrientation(id string) { *c = append(*c, id) }
func TestReopenOrientationKeepsOtherProjectsUnchanged(t *testing.T) {
	service := &Service{sessionStore: orientationStore{sessions: []*api.Session{nil, {ID: "target", ProjectID: "project"}, {ID: "foreign", ProjectID: "other"}, {ID: "second", ProjectID: "project"}}}}
	var changes orientationChanges
	service.ReopenOrientation(t.Context(), "project", &changes)
	if !reflect.DeepEqual(changes, orientationChanges{"target", "second"}) {
		t.Fatalf("orientation invalidations=%v", changes)
	}
}

func TestOrientationRefreshRejectsUnboundProjectAndMissingProjection(t *testing.T) {
	service := &Service{sessionStore: orientationStore{sessions: []*api.Session{{ID: "target", ProjectID: "project"}}}}
	var changes orientationChanges
	service.ReopenOrientation(t.Context(), "", &changes)
	service.ReopenOrientation(t.Context(), "project", nil)
	if len(changes) != 0 {
		t.Fatalf("unbound refresh invalidated sessions=%v", changes)
	}
}
