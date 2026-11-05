package naming

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type titleReadStore struct {
	Store
	session *api.Session
	err     error
	reads   int
}

func (s *titleReadStore) Get(context.Context, string) (*api.Session, error) {
	s.reads++
	return s.session, s.err
}

func TestExistingTitleSkipsInferenceFromStaleSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session *api.Session
		err     error
	}{
		{name: "named", session: &api.Session{ID: "s", Title: "Existing title"}},
		{name: "deleted"},
		{name: "unavailable", err: errors.New("store unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &titleReadStore{session: tc.session, err: tc.err}
			service := New(store, nil, nil, nil)
			service.SessionFromPrompt(t.Context(), &api.Session{ID: "s"}, "A follow-up request")
			if store.reads != 1 {
				t.Fatalf("store reads = %d, want one current title lookup", store.reads)
			}
		})
	}
}
