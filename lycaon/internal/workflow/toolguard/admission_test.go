package toolguard

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type boundSession struct {
	session *api.Session
	err     error
	id      string
}

func (s *boundSession) Get(_ context.Context, id string) (*api.Session, error) {
	s.id = id
	return s.session, s.err
}

func TestRequireSessionProjectUsesPersistedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session *api.Session
		err     error
		allow   bool
	}{{"same workspace", &api.Session{WorkspacePath: "/project"}, nil, true}, {"unbound session", &api.Session{}, nil, true}, {"different workspace", &api.Session{WorkspacePath: "/other"}, nil, false}, {"missing session", nil, nil, false}, {"store cancellation", nil, context.Canceled, false}} {
		t.Run(tc.name, func(t *testing.T) {
			store := &boundSession{session: tc.session, err: tc.err}
			ctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}}
			ctx.Source.Roots = []projectroot.RootRef{{ID: "root", Path: "/project", IsPrimary: true}}
			err := RequireSessionProject(t.Context(), store, ctx)
			if (err == nil) != tc.allow || store.id != "session" {
				t.Fatalf("session project admission=%v lookup=%q", err, store.id)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("store error lost: %v", err)
			}
		})
	}
}
