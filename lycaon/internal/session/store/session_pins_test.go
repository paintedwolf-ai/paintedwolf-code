package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type pinTestStore interface {
	Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
	CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error)
	Get(ctx context.Context, id string) (*api.Session, error)
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
	AppendMessages(ctx context.Context, id string, msgs ...api.Message) error
	PinSession(ctx context.Context, id string) error
	UnpinSession(ctx context.Context, id string) error
	MovePinnedSession(ctx context.Context, id string, position int) error
}

func pinTestStores(t *testing.T) map[string]pinTestStore {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	return map[string]pinTestStore{"sql": NewSQL(sqlDB), "memory": NewMemory()}
}

func createPinTestChats(t *testing.T, st pinTestStore, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create chat", err)
		ids = append(ids, sess.ID)
	}
	return ids
}

func pinRanks(t *testing.T, st pinTestStore, ids []string) []int {
	t.Helper()
	ranks := make([]int, len(ids))
	for i, id := range ids {
		sess, err := st.Get(t.Context(), id)
		testutil.FailErr(t, "get chat", err)
		if sess.PinRank != nil {
			ranks[i] = *sess.PinRank
		}
	}
	return ranks
}

func assertPinRanks(t *testing.T, st pinTestStore, ids []string, want ...int) {
	t.Helper()
	got := pinRanks(t, st, ids)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pin ranks = %v, want %v", got, want)
		}
	}
}

func TestPinOrder(t *testing.T) {
	for name, st := range pinTestStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ids := createPinTestChats(t, st, 4)
			for _, id := range ids[:3] {
				testutil.FailErr(t, "pin", st.PinSession(ctx, id))
			}
			assertPinRanks(t, st, ids, 1, 2, 3, 0)

			testutil.FailErr(t, "pin again", st.PinSession(ctx, ids[0]))
			assertPinRanks(t, st, ids, 1, 2, 3, 0)

			testutil.FailErr(t, "move last to top", st.MovePinnedSession(ctx, ids[2], 1))
			assertPinRanks(t, st, ids, 2, 3, 1, 0)

			testutil.FailErr(t, "move past the end", st.MovePinnedSession(ctx, ids[2], 50))
			assertPinRanks(t, st, ids, 1, 2, 3, 0)

			testutil.FailErr(t, "unpin", st.UnpinSession(ctx, ids[1]))
			assertPinRanks(t, st, ids, 1, 0, 3, 0)

			testutil.FailErr(t, "pin after a gap", st.PinSession(ctx, ids[3]))
			assertPinRanks(t, st, ids, 1, 0, 3, 4)

			testutil.FailErr(t, "move renumbers", st.MovePinnedSession(ctx, ids[3], 2))
			assertPinRanks(t, st, ids, 1, 0, 3, 2)

			if err := st.MovePinnedSession(ctx, ids[1], 1); !errors.Is(err, ErrSessionNotPinned) {
				t.Fatalf("moving an unpinned chat = %v, want ErrSessionNotPinned", err)
			}
			if err := st.PinSession(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("pinning a missing chat = %v, want ErrSessionNotFound", err)
			}
		})
	}
}

func TestPinStateFollowsLifecycle(t *testing.T) {
	for name, st := range pinTestStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ids := createPinTestChats(t, st, 2)
			testutil.FailErr(t, "pin", st.PinSession(ctx, ids[0]))
			testutil.FailErr(t, "rename", st.UpdateSession(ctx, ids[0], func(sess *api.Session) {
				sess.Title = "kept pinned"
			}))
			assertPinRanks(t, st, ids, 1, 0)

			testutil.FailErr(t, "archive", st.UpdateSession(ctx, ids[0], func(sess *api.Session) {
				now := time.Now().UTC()
				sess.ArchivedAt = &now
			}))
			assertPinRanks(t, st, ids, 0, 0)
			if err := st.PinSession(ctx, ids[0]); !errors.Is(err, ErrSessionArchived) {
				t.Fatalf("pinning an archived chat = %v, want ErrSessionArchived", err)
			}

			parent, err := st.Get(ctx, ids[1])
			testutil.FailErr(t, "get parent", err)
			child, err := st.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			if err := st.PinSession(ctx, child.ID); !errors.Is(err, ErrWorkerChildPin) {
				t.Fatalf("pinning a worker child = %v, want ErrWorkerChildPin", err)
			}
		})
	}
}

func TestActivityFollowsVisibleTranscript(t *testing.T) {
	for name, st := range pinTestStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ids := createPinTestChats(t, st, 1)
			id := ids[0]
			activity := func() time.Time {
				t.Helper()
				sess, err := st.Get(ctx, id)
				testutil.FailErr(t, "get chat", err)
				return sess.ActivityAt
			}
			created := activity()

			time.Sleep(2 * time.Millisecond)
			testutil.FailErr(t, "append internal row", st.AppendMessages(ctx, id, api.Message{
				Role: api.MessageRoleUser, Content: "host note", Visibility: api.MessageVisibilityInternal,
			}))
			testutil.FailErr(t, "mark seen", st.UpdateSession(ctx, id, func(sess *api.Session) {
				now := time.Now().UTC()
				sess.SeenAt = &now
				sess.Title = "renamed"
			}))
			testutil.FailErr(t, "pin", st.PinSession(ctx, id))
			if got := activity(); !got.Equal(created) {
				t.Fatalf("record changes moved activity_at from %s to %s", created, got)
			}

			time.Sleep(2 * time.Millisecond)
			testutil.FailErr(t, "append visible row", st.AppendMessages(ctx, id, api.Message{
				Role: api.MessageRoleUser, Content: "hello",
			}))
			if got := activity(); !got.After(created) {
				t.Fatalf("a visible message left activity_at at %s", got)
			}
		})
	}
}
