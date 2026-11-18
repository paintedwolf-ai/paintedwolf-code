package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type synthesisTestModel struct {
	callback func() (string, error)
	calls    int
}

func (s *synthesisTestModel) Summarize(context.Context, string, string, int) (string, error) {
	s.calls++
	return s.callback()
}

type synthesisLogRecord struct {
	Message    string `json:"msg"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
	Error      string `json:"error"`
	Generation int    `json:"generation"`
	AfterOrd   int64  `json:"summary_after_ord"`
	ThroughOrd int64  `json:"through_ord"`
	Calls      int    `json:"model_calls"`
}

func readSynthesisLog(t *testing.T, output string) []synthesisLogRecord {
	t.Helper()
	var records []synthesisLogRecord
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record synthesisLogRecord
		testutil.FailErr(t, "decode synthesis log", json.Unmarshal([]byte(line), &record))
		if record.Message == "continuation synthesis settled" {
			records = append(records, record)
		}
	}
	return records
}

func TestSynthesisLogsExactlyOneOutcomeWithCallCount(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, tc := range []struct {
		state  string
		reason string
		calls  int
	}{
		{"published", "", 2},
		{"stale after first call", "stale", 1},
		{"model changed", "model_changed", 0},
		{"no continuation", "no_continuation", 2},
		{"no summary part", "no_summary_part", 2},
		{"no growth allowed", "would_grow", 2},
		{"unchanged source", "no_advance", 0},
		{"provider error", "error", 1},
		{"canceled", "stale", 0},
	} {
		t.Run(tc.state, func(t *testing.T) {
			testSynthesisOutcome(t, tc.state, tc.reason, tc.calls)
		})
	}
}

func testSynthesisOutcome(t *testing.T, state, reason string, calls int) {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create synthesis session", err)
	testutil.FailErr(t, "append canonical observations", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "one", Role: api.MessageRoleAssistant, Content: "First observation"},
		api.Message{ID: "two", Role: api.MessageRoleAssistant, Content: "Second observation"}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical observations", err)
	second := rows[1]
	record := &messageview.Continuation{Format: messageview.ContinuationFormat, Window: 8192,
		ThroughOrd: second.Ord, Reason: "references_only"}
	baseline := synthesisTestBaseline(second, record, state)
	testutil.FailErr(t, "publish test baseline", mem.PutCompactionView(t.Context(), sess.ID, baseline))
	baseline.Continuation = record
	unit := &Service{store: mem, Gate: lifecycle.New(mem), Runner: NewRunner()}
	token, err := unit.Gate.Capture(t.Context(), sess.ID)
	testutil.FailErr(t, "capture session admission", err)
	model := synthesisOutcomeModel(t, mem, sess.ID, rows[0], state)
	cfg := compaction.DefaultCompactionConfig()
	cfg.SummaryInputTokens, cfg.SummaryMessageTokens = 900, 500
	c := compaction.NewSimpleCompactor(cfg, model)
	info := compaction.SessionInfo{ID: sess.ID}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	switch state {
	case "model changed":
		testutil.FailErr(t, "replace receiver model", mem.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Model = "changed" }))
	case "canceled":
		cancel()
	case "unchanged source":
		unit.runSynthesis(ctx, token, c, info, rows, baseline)
		if model.calls == 0 {
			t.Fatal("the first pass must attempt synthesis")
		}
		output.Reset()
		model.calls = 0
	}
	unit.runSynthesis(ctx, token, c, info, rows, baseline)
	logs := readSynthesisLog(t, output.String())
	expected := "published"
	if reason != "" {
		expected = "discarded"
	}
	if len(logs) != 1 {
		t.Fatalf("expected one outcome, got %+v", logs)
	}
	log := logs[0]
	if log.Outcome != expected || log.Reason != reason || log.Calls != calls || model.calls != calls ||
		log.Generation != 1 || log.AfterOrd != 0 || log.ThroughOrd != second.Ord ||
		(log.Error != "") != (reason == "error") {
		t.Fatalf("synthesis outcome = %+v, provider calls = %d", log, model.calls)
	}
}

func synthesisTestBaseline(last api.Message, record *messageview.Continuation, state string) store.CompactionView {
	text := strings.Repeat("Prior continuation content. ", 125)
	if state == "no growth allowed" || state == "unchanged source" {
		text = "short"
	}
	view := store.CompactionView{Generation: 1, CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: last.Seq,
		Continuation: record, Messages: []api.Message{{ID: "record", Role: api.MessageRoleAssistant, Content: text,
			ContentParts: []api.MessageContentPart{{Content: text, Source: "compaction_summary"}}}}}
	switch state {
	case "no continuation":
		view.Continuation = nil
	case "no summary part":
		view.Messages[0].ContentParts = nil
	}
	return view
}

func synthesisOutcomeModel(t *testing.T, mem *store.Memory, sessionID string, first api.Message, state string) *synthesisTestModel {
	t.Helper()
	return &synthesisTestModel{callback: func() (string, error) {
		switch state {
		case "stale after first call":
			first.Content = "Corrected first observation"
			_, err := mem.UpdateMessage(t.Context(), sessionID, first.ID, first)
			testutil.FailErr(t, "make source stale between pages", err)
		case "provider error":
			return "", errors.New("fixture provider error")
		}
		return `{"facts":["Accepted observation"],"completed":[],"pending":[],"reacquire":[],"constraints":[]}`, nil
	}}
}
