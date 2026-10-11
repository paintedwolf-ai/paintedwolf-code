//go:build integration

package property

import (
	"context"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func TestStatusRuntimeDerivedNeverPersisted(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "status-runtime.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	var liveColumns, draftColumns int
	testutil.FailErr(t, "inspect message liveness storage", sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('messages') WHERE name IN ('status', 'generating_tokens', 'live_status')`).Scan(&liveColumns))
	testutil.FailErr(t, "inspect durable draft storage", sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('messages') WHERE name = 'draft_status'`).Scan(&draftColumns))
	if liveColumns != 0 || draftColumns != 1 {
		t.Fatalf("message liveness columns = %d, durable draft columns = %d", liveColumns, draftColumns)
	}
	store := store.NewSQL(sqlDB)
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)

	rapid.Check(t, func(t *rapid.T) {
		sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(t, "create isolated session", err)

		count := rapid.IntRange(1, 5).Draw(t, "count")
		ids := make([]string, 0, count)
		for i := 0; i < count; i++ {
			id := sess.ID + "-message-" + strconv.Itoa(i)
			ids = append(ids, id)
			failErr(t, "append", store.AppendMessages(ctx, sess.ID, api.Message{
				ID:          id,
				Role:        api.MessageRoleAssistant,
				Content:     rapid.StringMatching(`[a-z ]{0,20}`).Draw(t, "body"),
				DraftStatus: rapid.SampledFrom([]api.DraftStatus{"", api.DraftStatusLive, api.DraftStatusCommitted}).Draw(t, "draft"),
			}))
		}

		page, err := mgr.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{})
		failErr(t, "hydrate", err)
		if len(page.Messages) != count {
			t.Fatalf("hydrate count = %d want %d", len(page.Messages), count)
		}
		ords := make([]int64, len(page.Messages))
		for i, msg := range page.Messages {
			ords[i] = msg.Ord
			if msg.Status != api.MessageLiveStatusComplete {
				t.Fatalf("hydrate %s status = %q want complete (no active turn)", msg.ID, msg.Status)
			}
		}

		activeIdx := rapid.IntRange(0, count-1).Draw(t, "active")
		activeID := ids[activeIdx]
		tokens := rapid.IntRange(0, 4096).Draw(t, "tokens")
		mgr.Runner.Transcript.Streams.SetActive(sess.ID, activeID, tokens)

		livePage, err := mgr.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{})
		failErr(t, "live page", err)
		if len(livePage.Messages) != count {
			t.Fatalf("live count = %d want %d (status toggle must not change existence)", len(livePage.Messages), count)
		}
		for i, msg := range livePage.Messages {
			if msg.Ord != ords[i] {
				t.Fatalf("ord mutated at %d: was %d now %d", i, ords[i], msg.Ord)
			}
			if msg.ID == activeID {
				if msg.Status != api.MessageLiveStatusStreaming {
					t.Fatalf("active %s status = %q want streaming", msg.ID, msg.Status)
				}
				if msg.GeneratingTokens != tokens {
					t.Fatalf("generating_tokens = %d want %d", msg.GeneratingTokens, tokens)
				}
			} else if msg.Status != api.MessageLiveStatusComplete {
				t.Fatalf("inactive %s status = %q want complete", msg.ID, msg.Status)
			}
		}

		mgr.Runner.Transcript.Streams.Finish(ctx, sess.ID)
		settled, err := mgr.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{})
		failErr(t, "settled page", err)
		if len(settled.Messages) != count {
			t.Fatalf("settled count = %d want %d", len(settled.Messages), count)
		}
		for _, msg := range settled.Messages {
			if msg.Status != api.MessageLiveStatusComplete {
				t.Fatalf("cleared %s status = %q want complete", msg.ID, msg.Status)
			}
		}
	})
}
