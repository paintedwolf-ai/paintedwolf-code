package store

import (
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func pinTestStores(t *testing.T) map[string]*Repositories {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	return map[string]*Repositories{"sql": NewSQL(sqlDB)}
}

func createPinTestChats(t *testing.T, st *Repositories, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create chat", err)
		ids = append(ids, sess.ID)
	}
	return ids
}

func pinRanks(t *testing.T, st *Repositories, ids []string) []int {
	t.Helper()
	ranks := make([]int, len(ids))
	for i, id := range ids {
		sess, err := st.Sessions.Get(t.Context(), id)
		testutil.FailErr(t, "get chat", err)
		if sess.PinRank != nil {
			ranks[i] = *sess.PinRank
		}
	}
	return ranks
}

func assertPinRanks(t *testing.T, st *Repositories, ids []string, want ...int) {
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
				testutil.FailErr(t, "pin", st.Sessions.PinSession(ctx, id))
			}
			assertPinRanks(t, st, ids, 1, 2, 3, 0)

			testutil.FailErr(t, "pin again", st.Sessions.PinSession(ctx, ids[0]))
			assertPinRanks(t, st, ids, 1, 2, 3, 0)

			testutil.FailErr(t, "move last to top", st.Sessions.MovePinnedSession(ctx, ids[2], 1))
			assertPinRanks(t, st, ids, 2, 3, 1, 0)

			testutil.FailErr(t, "move past the end", st.Sessions.MovePinnedSession(ctx, ids[2], 50))
			assertPinRanks(t, st, ids, 1, 2, 3, 0)

			testutil.FailErr(t, "unpin", st.Sessions.UnpinSession(ctx, ids[1]))
			assertPinRanks(t, st, ids, 1, 0, 3, 0)

			testutil.FailErr(t, "pin after a gap", st.Sessions.PinSession(ctx, ids[3]))
			assertPinRanks(t, st, ids, 1, 0, 3, 4)

			testutil.FailErr(t, "move renumbers", st.Sessions.MovePinnedSession(ctx, ids[3], 2))
			assertPinRanks(t, st, ids, 1, 0, 3, 2)

			if err := st.Sessions.MovePinnedSession(ctx, ids[1], 1); !errors.Is(err, ErrSessionNotPinned) {
				t.Fatalf("moving an unpinned chat = %v, want ErrSessionNotPinned", err)
			}
			if err := st.Sessions.PinSession(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
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
			testutil.FailErr(t, "pin", st.Sessions.PinSession(ctx, ids[0]))
			testutil.FailErr(t, "rename", st.Sessions.UpdateSession(ctx, ids[0], func(sess *api.Session) {
				sess.Title = "kept pinned"
			}))
			assertPinRanks(t, st, ids, 1, 0)

			testutil.FailErr(t, "archive", st.Sessions.UpdateSession(ctx, ids[0], func(sess *api.Session) {
				now := time.Now().UTC()
				sess.ArchivedAt = &now
			}))
			assertPinRanks(t, st, ids, 0, 0)
			if err := st.Sessions.PinSession(ctx, ids[0]); !errors.Is(err, ErrSessionArchived) {
				t.Fatalf("pinning an archived chat = %v, want ErrSessionArchived", err)
			}

			parent, err := st.Sessions.Get(ctx, ids[1])
			testutil.FailErr(t, "get parent", err)
			child, err := st.Sessions.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			if err := st.Sessions.PinSession(ctx, child.ID); !errors.Is(err, ErrWorkerChildPin) {
				t.Fatalf("pinning a worker child = %v, want ErrWorkerChildPin", err)
			}
		})
	}
}

func TestActivityFollowsVisibleTranscript(t *testing.T) {
	for name, st := range pinTestStores(t) {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				ids := createPinTestChats(t, st, 1)
				id := ids[0]
				activity := func() time.Time {
					t.Helper()
					sess, err := st.Sessions.Get(ctx, id)
					testutil.FailErr(t, "get chat", err)
					return sess.ActivityAt
				}
				created := activity()

				time.Sleep(2 * time.Millisecond)
				testutil.FailErr(t, "append internal row", st.Transcript.AppendMessages(ctx, id, api.Message{
					Role: api.MessageRoleUser, Content: "host note", Visibility: api.MessageVisibilityInternal,
				}))
				testutil.FailErr(t, "mark seen", st.Sessions.UpdateSession(ctx, id, func(sess *api.Session) {
					now := time.Now().UTC()
					sess.SeenAt = &now
					sess.Title = "renamed"
				}))
				testutil.FailErr(t, "pin", st.Sessions.PinSession(ctx, id))
				if got := activity(); !got.Equal(created) {
					t.Fatalf("record changes moved activity_at from %s to %s", created, got)
				}

				time.Sleep(2 * time.Millisecond)
				testutil.FailErr(t, "append visible row", st.Transcript.AppendMessages(ctx, id, api.Message{
					Role: api.MessageRoleUser, Content: "hello",
				}))
				if got := activity(); !got.After(created) {
					t.Fatalf("a visible message left activity_at at %s", got)
				}
			})
		})
	}
}
