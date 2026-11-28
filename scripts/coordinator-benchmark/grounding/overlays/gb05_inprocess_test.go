package promptloop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/pkg/api"
)

const gb05PublishingSurface = "implement_routing"

type gb05WordingVariant struct {
	name   string
	prefix string
}

var gb05D5Wordings = []gb05WordingVariant{
	{name: "confident", prefix: "Confirmed:"},
	{name: "hedged", prefix: "It appears likely that"},
	{name: "completion_asserting", prefix: "All checks are complete and verified:"},
}

type gb05LedgerStub struct {
	records []evidence.Record
}

func (s *gb05LedgerStub) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.AssembleLedger(s.records), nil
}

func (s *gb05LedgerStub) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

type gb05MidTurnHarness struct {
	loop       *PromptLoop
	ledger     *gb05LedgerStub
	sess       *api.Session
	budget     *closeouts.Service
	appendFn   func(api.Message) error
	appended   []api.Message
	order      []string
	notices    []string
	friction   int
}

func newGB05MidTurnHarness(t *testing.T, sessionID string) *gb05MidTurnHarness {
	t.Helper()
	h := &gb05MidTurnHarness{
		ledger: &gb05LedgerStub{},
		sess:   &api.Session{ID: sessionID, Posture: api.SessionPostureBuild},
		budget: closeouts.New(nil),
	}
	reg := toolstest.NewRegistry()
	testutil.FailErr(t, "register recall", reg.Register("recall", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		h.order = append(h.order, "recall")
		return `{"ok":true}`, nil
	}))
	h.loop = NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{Tools: reg},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				for _, msg := range msgs {
					if h.appendFn != nil {
						if err := h.appendFn(msg); err != nil {
							return err
						}
					}
					h.appended = append(h.appended, msg)
					if api.IsAgentNoteMessage(msg) {
						h.order = append(h.order, "note")
					}
				}
				return nil
			},
		},
		Closeout: CloseoutDeps{
			EvidenceLedger: h.ledger,
			EvaluateMidTurnNote: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) error {
				h.notices = append(h.notices, gc.Rejection.RejectObservation)
				return nil
			},
			CloseoutTrackingDeps: CloseoutTrackingDeps{
				RecordGroundingFriction: func(ctx context.Context, sid string) guidance.GroundingFriction {
					h.friction++
					return h.budget.RecordGroundingFriction(ctx, sid)
				},
			},
		},
	})
	return h
}

func gb05TurnState(t *testing.T, surfaceID string) *promptLoopTurnState {
	t.Helper()
	st := &promptLoopTurnState{}
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "compile tool plans", err)
	st.turnToolPlan = plans[surfaceID]
	return st
}

func gb05CountNotes(history []api.Message, carrierID string) int {
	noteID := api.MidTurnNoteID(carrierID)
	count := 0
	for _, msg := range history {
		if msg.ID == noteID {
			count++
		}
	}
	return count
}

// TestGB05InProcessMidTurnCrash covers the 2 GB05 in-process slots in ledger.md
// across all 3 D5 wording variants (6 subtests total):
// - S06-14-inprocess-crash-after-carrier-before-note
// - S06-15-inprocess-crash-after-note-before-ack
func TestGB05InProcessMidTurnCrash(t *testing.T) {
	t.Run("S06-14-inprocess-crash-after-carrier-before-note", func(t *testing.T) {
		type outcomeSig struct {
			crashedToolCount   int
			crashedUnpublished bool
			recoveredNotes     int
			recoveredOrder     string
			friction           int
		}
		var sigs []outcomeSig

		for _, w := range gb05D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				h := newGB05MidTurnHarness(t, "gb05-s06-14-"+w.name)
				errCrash := errors.New("simulated crash between carrier commit and mid-turn note persist")
				crashActive := true
				h.appendFn = func(msg api.Message) error {
					if crashActive && api.IsAgentNoteMessage(msg) {
						return errCrash
					}
					return nil
				}

				carrierID := "carrier-s06-14-" + w.name
				noteText := w.prefix + " Inspecting service.go before running recall."
				calls := []api.ToolCall{{ID: "tc-recall-1", Name: "recall"}}
				history := []api.Message{
					{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect service.go"},
					{ID: carrierID, Role: api.MessageRoleAssistant, Content: noteText, ToolCalls: calls},
				}

				// 1. Crash occurs after carrier message is in history, before note commit succeeds.
				crashed, err := h.loop.Batch.executeToolCallsInTurn(
					context.Background(),
					h.sess,
					h.sess.ID,
					calls,
					tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: h.sess.ID}},
					history,
					"Inspect service.go",
					carrierID,
					gb05PublishingSurface,
					gb05TurnState(t, gb05PublishingSurface),
				)
				if !errors.Is(err, errCrash) {
					t.Fatalf("expected simulated crash error, got %v", err)
				}
				if len(h.order) != 0 {
					t.Fatalf("no tools or notes may complete after note-commit crash; order=%v", h.order)
				}
				if gb05CountNotes(crashed.history, carrierID) != 0 {
					t.Fatalf("failed note commit must not appear in history")
				}
				crashedUnpub := guard.DeriveWaitVisibility(crashed.history).Unpublished
				if !crashedUnpub {
					t.Fatalf("crashed unpublished carrier must remain owed in wait visibility")
				}

				// 2. Recovery replay after restart once storage is healthy.
				crashActive = false
				recovered, err := h.loop.Batch.executeToolCallsInTurn(
					context.Background(),
					h.sess,
					h.sess.ID,
					calls,
					tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: h.sess.ID}},
					crashed.history,
					"Inspect service.go",
					carrierID,
					gb05PublishingSurface,
					gb05TurnState(t, gb05PublishingSurface),
				)
				testutil.FailErr(t, "recovery replay after carrier-before-note crash", err)

				noteCount := gb05CountNotes(recovered.history, carrierID)
				orderStr := strings.Join(h.order, ",")
				if noteCount != 1 {
					t.Fatalf("expected exactly 1 committed note after recovery, got %d", noteCount)
				}
				if orderStr != "note,recall" {
					t.Fatalf("expected note before recall on recovery, got %q", orderStr)
				}
				if guard.DeriveWaitVisibility(recovered.history).Unpublished {
					t.Fatalf("recovered carrier must no longer be unpublished")
				}
				if h.friction != 0 {
					t.Fatalf("crash recovery must charge 0 friction, got %d", h.friction)
				}

				sigs = append(sigs, outcomeSig{
					crashedToolCount:   0,
					crashedUnpublished: crashedUnpub,
					recoveredNotes:     noteCount,
					recoveredOrder:     orderStr,
					friction:           h.friction,
				})
			})
		}

		if len(sigs) != len(gb05D5Wordings) {
			t.Fatalf("expected %d wording runs, got %d", len(gb05D5Wordings), len(sigs))
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on S06-14: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})

	t.Run("S06-15-inprocess-crash-after-note-before-ack", func(t *testing.T) {
		type outcomeSig struct {
			dupNoteCount      int
			dupOrder          string
			reloadedNoteCount int
			reloadedOrder     string
			friction          int
		}
		var sigs []outcomeSig

		for _, w := range gb05D5Wordings {
			t.Run(w.name, func(t *testing.T) {
				carrierID := "carrier-s06-15-" + w.name
				noteText := w.prefix + " Persisted note before crash."
				calls := []api.ToolCall{{ID: "tc-recall-1", Name: "recall"}}

				// Phase A: Note persisted on disk before crash; replay receives ErrDuplicateMessageID.
				hDup := newGB05MidTurnHarness(t, "gb05-s06-15-dup-"+w.name)
				hDup.appendFn = func(msg api.Message) error {
					if api.IsAgentNoteMessage(msg) {
						return store.ErrDuplicateMessageID
					}
					return nil
				}
				historyDup := []api.Message{
					{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect service.go"},
					{ID: carrierID, Role: api.MessageRoleAssistant, Content: noteText, ToolCalls: calls},
				}
				outDup, err := hDup.loop.Batch.executeToolCallsInTurn(
					context.Background(),
					hDup.sess,
					hDup.sess.ID,
					calls,
					tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: hDup.sess.ID}},
					historyDup,
					"Inspect service.go",
					carrierID,
					gb05PublishingSurface,
					gb05TurnState(t, gb05PublishingSurface),
				)
				testutil.FailErr(t, "duplicate note replay", err)
				dupNotes := gb05CountNotes(outDup.history, carrierID)
				dupOrder := strings.Join(hDup.order, ",")
				if dupNotes != 1 || dupOrder != "recall" {
					t.Fatalf("phase A: notes=%d order=%q want notes=1 order=recall", dupNotes, dupOrder)
				}
				if hDup.friction != 0 {
					t.Fatalf("phase A: expected 0 friction, got %d", hDup.friction)
				}

				// Phase B: Restart reloaded history already containing MidTurnNoteID(carrierID).
				hReload := newGB05MidTurnHarness(t, "gb05-s06-15-reload-"+w.name)
				existingNote := api.Message{
					ID:         api.MidTurnNoteID(carrierID),
					Role:       api.MessageRoleAssistant,
					Kind:       api.MessageKindAgentNote,
					Visibility: api.MessageVisibilityTranscript,
					Content:    noteText,
				}
				historyReload := []api.Message{
					{ID: "u1", Role: api.MessageRoleUser, Content: "Inspect service.go"},
					{ID: carrierID, Role: api.MessageRoleAssistant, Content: noteText, ToolCalls: calls},
					existingNote,
				}
				outReload, err := hReload.loop.Batch.executeToolCallsInTurn(
					context.Background(),
					hReload.sess,
					hReload.sess.ID,
					calls,
					tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: hReload.sess.ID}},
					historyReload,
					"Inspect service.go",
					carrierID,
					gb05PublishingSurface,
					gb05TurnState(t, gb05PublishingSurface),
				)
				testutil.FailErr(t, "reloaded history replay", err)
				reloadedNotes := gb05CountNotes(outReload.history, carrierID)
				reloadedOrder := strings.Join(hReload.order, ",")
				if reloadedNotes != 1 || reloadedOrder != "recall" {
					t.Fatalf("phase B: notes=%d order=%q want notes=1 order=recall", reloadedNotes, reloadedOrder)
				}
				if hReload.friction != 0 {
					t.Fatalf("phase B: expected 0 friction, got %d", hReload.friction)
				}

				sigs = append(sigs, outcomeSig{
					dupNoteCount:      dupNotes,
					dupOrder:          dupOrder,
					reloadedNoteCount: reloadedNotes,
					reloadedOrder:     reloadedOrder,
					friction:          hDup.friction + hReload.friction,
				})
			})
		}

		if len(sigs) != len(gb05D5Wordings) {
			t.Fatalf("expected %d wording runs, got %d", len(gb05D5Wordings), len(sigs))
		}
		for i := 1; i < len(sigs); i++ {
			if sigs[i] != sigs[0] {
				t.Fatalf("D5 wording divergence on S06-15: %+v vs %+v", sigs[i], sigs[0])
			}
		}
	})
}
