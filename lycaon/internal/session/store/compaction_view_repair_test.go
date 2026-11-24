package store

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDamagedCurrentFormatViewRejectedForCanonicalRebuild(t *testing.T) {
	database := testdbfixture.Open(t, "damaged-continuation-view.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)

	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append canonical row", st.Transcript.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "msg-1", Role: api.MessageRoleAssistant, Content: strings.Repeat("canonical observation ", 40)}))
	rows, err := st.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical row", err)
	last := rows[len(rows)-1]

	// Valid chunk-only view without Continuation remains readable.
	chunkOnly := CompactionView{
		Generation:        1,
		CoveredThroughOrd: last.Ord,
		CoveredThroughID:  last.ID,
		SourceSeq:         last.Seq,
		Messages: []api.Message{{
			ID:             "msg-1",
			Ord:            last.Ord,
			Role:           api.MessageRoleAssistant,
			Content:        "compacted ordinary row",
			ContentParts:   []api.MessageContentPart{{Content: "compacted ordinary row", Source: "compacted_chunk"}},
			CompactedChunk: &api.CompactedChunkMeta{OriginalTokens: 2000, CompactedTokens: 200, Strategy: "truncate"},
		}},
	}
	testutil.FailErr(t, "put chunk-only view", st.Compaction.PutCompactionView(t.Context(), sess.ID, chunkOnly))
	if _, ok, err := st.Compaction.GetCompactionView(t.Context(), sess.ID); err != nil || !ok {
		t.Fatalf("valid chunk-only view rejected: ok=%v err=%v", ok, err)
	}

	// Damaged Format=1 view: Continuation is present, but the summary row was rewritten to compacted_chunk.
	damaged := CompactionView{
		Generation:        2,
		CoveredThroughOrd: last.Ord,
		CoveredThroughID:  last.ID,
		SourceSeq:         last.Seq,
		Continuation: &messageview.Continuation{
			Format:     messageview.ContinuationFormat,
			ThroughOrd: last.Ord,
			Reason:     "summarized",
		},
		Messages: []api.Message{
			{
				ID:                   "cp-1",
				Role:                 api.MessageRoleUser,
				Content:              "Prior conversation compacted.",
				CompactionCheckpoint: true,
				ContentParts:         []api.MessageContentPart{{Content: "Prior conversation compacted.", Source: "compaction"}},
			},
			{
				ID:             "sum-1",
				Role:           api.MessageRoleAssistant,
				Content:        "truncated continuation",
				ContentParts:   []api.MessageContentPart{{Content: "truncated continuation", Source: "compacted_chunk"}},
				CompactedChunk: &api.CompactedChunkMeta{OriginalTokens: 2000, CompactedTokens: 200, Strategy: "truncate"},
			},
		},
	}
	testutil.FailErr(t, "put damaged view", st.Compaction.PutCompactionView(t.Context(), sess.ID, damaged))

	reopened := NewSQL(database)
	if _, ok, err := reopened.Compaction.GetCompactionView(t.Context(), sess.ID); err != nil || ok {
		t.Fatalf("damaged current-format view was accepted: ok=%v err=%v", ok, err)
	}
}
