package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionEventProjectIdentitySurvivesWorkspaceChanges(t *testing.T) {
	projectID := eventFixtureProject(t)
	sess := &api.Session{ID: "session", ProjectID: projectID}
	for _, path := range []string{t.TempDir(), t.TempDir(), ""} {
		sess.WorkspacePath = path
		key := events.PublishKey{Project: sessionProjectKey(sess), Session: sess.ID}
		if key.Project != projectID {
			t.Fatalf("workspace %q changed project identity to %q, want %q", path, key.Project, projectID)
		}
		testutil.FailErr(t, "validate session event scope", events.ValidatePublishScope(api.EventTopicSession, key))
	}
}

func TestSessionEventRequiresPersistedProjectIdentity(t *testing.T) {
	for _, sess := range []*api.Session{nil, {ID: "session", WorkspacePath: t.TempDir()}, {ID: "session"}} {
		key := events.PublishKey{Project: sessionProjectKey(sess), Session: "session"}
		if key.Project != "" {
			t.Fatalf("session without persisted project produced identity %q", key.Project)
		}
		if err := events.ValidatePublishScope(api.EventTopicSession, key); err == nil {
			t.Fatalf("session without persisted project admitted event: %+v", key)
		}
	}
}
