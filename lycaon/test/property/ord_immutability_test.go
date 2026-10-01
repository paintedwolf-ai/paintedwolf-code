//go:build integration

package property

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func TestOrdCreatedAtImmutabilityAcrossPatches(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "ord-property.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := store.NewSQL(sqlDB)

	rapid.Check(t, func(t *rapid.T) {
		sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(t, "create isolated session", err)
		id := uuid.NewString()
		created := time.Date(2026, 7, 8, 12, 0, rapid.IntRange(0, 59).Draw(t, "created_sec"), 0, time.UTC)
		failErr(t, "append", store.AppendMessages(ctx, sess.ID, api.Message{
			ID:        id,
			Role:      api.MessageRoleAssistant,
			Content:   "seed body",
			CreatedAt: created,
		}))

		before := mustFindMessage(t, ctx, store, sess.ID, id)
		if before.Ord == 0 {
			t.Fatalf("ord not minted at append: %#v", before)
		}

		// Exercise both zero and conflicting patch timestamps.
		patchTS := time.Time{}
		if !rapid.Bool().Draw(t, "patch_ts_zero") {
			patchTS = time.Date(2025, 1, 1, 0, 0, rapid.IntRange(0, 59).Draw(t, "patch_sec"), 0, time.UTC)
		}
		patch := api.Message{
			ID:                id,
			Role:              rapid.SampledFrom([]api.MessageRole{api.MessageRoleUser, api.MessageRoleAssistant, api.MessageRoleTool, api.MessageRoleSystem}).Draw(t, "role"),
			Content:           rapid.StringMatching(`[a-z .,]{0,40}`).Draw(t, "content"),
			Kind:              rapid.SampledFrom([]api.MessageKind{"", api.MessageKindDraft, api.MessageKindWorkflowBoundary, api.MessageKindProgressUpdate}).Draw(t, "kind"),
			Visibility:        rapid.SampledFrom(api.AllMessageVisibilities()).Draw(t, "visibility"),
			DraftStatus:       rapid.SampledFrom(append([]api.DraftStatus{""}, api.AllDraftStatuses()...)).Draw(t, "draft_status"),
			DraftVersionCount: rapid.IntRange(0, 5).Draw(t, "draft_version_count"),
			CreatedAt:         patchTS,
		}
		_, err = store.UpdateMessage(ctx, sess.ID, id, patch)
		failErr(t, "patch", err)

		after := mustFindMessage(t, ctx, store, sess.ID, id)
		if after.Ord != before.Ord {
			t.Fatalf("ord mutated by patch: was %d, now %d — ord is the immutable creation ordinal", before.Ord, after.Ord)
		}
		if !after.CreatedAt.Equal(before.CreatedAt) {
			t.Fatalf("created_at mutated by patch: was %v, now %v (patch value was %v) — created_at is immutable on patch", before.CreatedAt, after.CreatedAt, patchTS)
		}
		if after.Seq <= before.Seq {
			t.Fatalf("seq did not advance across patch: was %d, now %d — seq is the mutation clock", before.Seq, after.Seq)
		}

		// Hydration follows the immutable ordinal.
		all, err := store.GetMessages(ctx, sess.ID)
		failErr(t, "get messages", err)
		for i := 1; i < len(all); i++ {
			if all[i].Ord <= all[i-1].Ord {
				t.Fatalf("hydration not in ord order at %d: ord %d <= %d — every ordering path reads ord", i, all[i].Ord, all[i-1].Ord)
			}
		}
	})
}
