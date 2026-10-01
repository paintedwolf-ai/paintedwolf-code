package store

import (
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestChunkProjectionSurvivesUnrelatedViewInvalidation(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			var transcript compactionMutationStore
			var projections messageview.ChunkProjectionStore
			if backend == "memory" {
				st := NewMemory()
				transcript, projections = st, st
			} else {
				database := testdbfixture.Open(t, "chunks.db")
				testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
				st := NewSQL(database)
				transcript, projections = st, st
			}
			sess, err := transcript.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "append source rows", transcript.AppendMessages(t.Context(), sess.ID,
				api.Message{ID: "source", Role: api.MessageRoleUser, Content: "source"},
				api.Message{ID: "unrelated", Role: api.MessageRoleAssistant, Content: "old reply"}))
			rows, err := transcript.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "load covered rows", err)
			testutil.FailErr(t, "install full view", transcript.PutCompactionView(t.Context(), sess.ID, CompactionView{
				Generation: 1, CoveredThroughOrd: rows[1].Ord, CoveredThroughID: rows[1].ID,
				SourceSeq: rows[1].Seq, Messages: rows,
			}))
			projection := messageview.ChunkProjection{Revision: "revision", Content: "summary", Meta: &api.CompactedChunkMeta{Strategy: "summarize"}}
			testutil.FailErr(t, "store reusable summary", projections.PutChunkProjection(t.Context(), sess.ID, "source", projection))
			_, err = transcript.UpdateMessage(t.Context(), sess.ID, "unrelated", api.Message{Role: api.MessageRoleAssistant, Content: "new reply"})
			testutil.FailErr(t, "edit unrelated source", err)
			_, exists, err := transcript.GetCompactionView(t.Context(), sess.ID)
			testutil.FailErr(t, "check invalidated view", err)
			if exists {
				t.Fatal("covered edit did not invalidate the full view")
			}
			got, ok, err := projections.GetChunkProjection(t.Context(), sess.ID, "source")
			testutil.FailErr(t, "read reusable summary", err)
			if !ok || got.Revision != "revision" || got.Content != "summary" {
				t.Fatalf("lost reusable summary: %+v", got)
			}
			got.Meta.Strategy = "changed by caller"
			again, _, err := projections.GetChunkProjection(t.Context(), sess.ID, "source")
			testutil.FailErr(t, "read independent snapshot", err)
			if again.Meta.Strategy != "summarize" {
				t.Fatal("projection aliases caller-owned memory")
			}
			testutil.FailErr(t, "remember no reduction", projections.PutChunkProjection(t.Context(), sess.ID, "source", messageview.ChunkProjection{Revision: "new-revision"}))
			got, ok, err = projections.GetChunkProjection(t.Context(), sess.ID, "source")
			testutil.FailErr(t, "read no-reduction receipt", err)
			if !ok || got.Meta != nil || got.Revision != "new-revision" {
				t.Fatalf("no-reduction receipt = %+v", got)
			}
		})
	}
}
