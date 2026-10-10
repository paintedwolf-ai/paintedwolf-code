package chats

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"testing"
)

type orientationStore struct {
	Store
	sessions []*api.Session
}

func (s orientationStore) List(context.Context) ([]*api.Session, error) { return s.sessions, nil }

type orientationChanges []string

func (c *orientationChanges) InvalidateOrientation(id string) { *c = append(*c, id) }
func TestReopenOrientationKeepsOtherProjectsUnchanged(t *testing.T) {
	service := &Service{store: orientationStore{sessions: []*api.Session{nil, {ID: "target", ProjectID: "project"}, {ID: "foreign", ProjectID: "other"}, {ID: "second", ProjectID: "project"}}}}
	var changes orientationChanges
	service.ReopenOrientation(t.Context(), "project", &changes)
	if !reflect.DeepEqual(changes, orientationChanges{"target", "second"}) {
		t.Fatalf("orientation invalidations=%v", changes)
	}
}
