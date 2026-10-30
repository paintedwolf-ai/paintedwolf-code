package history

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreparedReductionPublicationRequiresItsCapturedState(t *testing.T) {
	for _, changed := range []string{"none", "source", "projection", "model", "cancellation"} {
		t.Run(changed, func(t *testing.T) {
			mem := store.NewMemory()
			sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "append source", mem.AppendMessages(t.Context(), sess.ID, api.Message{ID: "source", Role: "assistant", Content: strings.Repeat("original observation ", 100)}))
			rows, err := mem.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read source", err)
			source := rows[0]
			snapshot := store.CompactionView{CoveredThroughOrd: source.Ord, CoveredThroughID: source.ID, SourceSeq: source.Seq}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch changed {
			case "source":
				source.Content = "corrected observation"
				_, err = mem.UpdateMessage(t.Context(), sess.ID, source.ID, source)
			case "projection":
				newer := snapshot
				newer.Generation = 1
				err = mem.PutCompactionView(t.Context(), sess.ID, newer)
			case "model":
				err = mem.UpdateSession(t.Context(), sess.ID, func(s *api.Session) { s.Model = "different-model" })
			case "cancellation":
				cancel()
			}
			testutil.FailErr(t, "change captured state", err)
			report := compaction.CompactionReport{SessionCompacted: true, TokensBefore: 1000, TokensAfter: 20,
				Continuation: &messageview.Continuation{Format: 1, ThroughOrd: source.Ord}}
			svc := &Service{store: mem}
			err = svc.publishReductionSynthesis(ctx, compaction.SessionInfo{ID: sess.ID}, rows, snapshot,
				[]compaction.ContextMessage{{ID: "summary", Role: "assistant", Content: "A concise observation."}}, report)
			if changed == "cancellation" {
				if err != context.Canceled {
					t.Fatalf("cancelled publication returned %v", err)
				}
			} else {
				testutil.FailErr(t, "publish prepared reduction", err)
			}
			current, err := mem.Get(t.Context(), sess.ID)
			testutil.FailErr(t, "read current generation", err)
			want := 0
			if changed == "none" || changed == "projection" {
				want = 1
			}
			if current.CompactionGeneration != want {
				t.Fatalf("generation = %d, want %d", current.CompactionGeneration, want)
			}
		})
	}
}

func TestOptionalEnrichmentCannotExpandTheAcceptedProjection(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append source", mem.AppendMessages(t.Context(), sess.ID, api.Message{ID: "source", Role: "assistant", Content: "recorded observation"}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read source", err)
	source := rows[0]
	record := &messageview.Continuation{Format: 1, ThroughOrd: source.Ord, Window: 8192}
	snapshot := store.CompactionView{Generation: 1, CoveredThroughOrd: source.Ord, CoveredThroughID: source.ID, SourceSeq: source.Seq, Continuation: record,
		Messages: []api.Message{{ID: "summary", Role: "assistant", Content: "small", ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: "small"}}}}}
	testutil.FailErr(t, "publish compact baseline", mem.PutCompactionView(t.Context(), sess.ID, snapshot))
	enriched := messageview.CloneContinuation(record)
	enriched.SummaryThroughOrd = source.Ord
	enriched.Summary.Facts = []string{strings.Repeat("larger generated continuation ", 100)}
	svc := &Service{store: mem}
	testutil.FailErr(t, "reject growing enrichment", svc.publishSynthesis(t.Context(), compaction.SessionInfo{ID: sess.ID}, snapshot, enriched))
	current, _, err := mem.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "read preserved baseline", err)
	if current.Generation != 1 || current.Messages[0].Content != "small" {
		t.Fatal("optional enrichment expanded an accepted projection")
	}
}
