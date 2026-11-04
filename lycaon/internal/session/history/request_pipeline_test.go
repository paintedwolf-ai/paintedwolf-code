package history

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// wireRequest takes one request through loading, assembly, fitting, and the real
// provider serialization, as a prompt turn does after each tool batch.
type wireRequest struct {
	svc      *Service
	sess     *api.Session
	provider *openaicompat.Provider
	frame    string
	budgets  fixedBudgets
}

func (w wireRequest) render(t *testing.T) ([]api.Message, []string) {
	t.Helper()
	ctx := t.Context()
	loaded, err := w.svc.Load(ctx, w.sess)
	testutil.FailErr(t, "load", err)
	assembled, _ := w.svc.Assemble(ctx, w.sess, loaded, "")
	req := requestFor(w.sess, w.frame, transcript.Project(api.FilterPromptHistory(assembled)))
	req.Model = "fixture-model"
	fitted, err := w.svc.FitRequest(ctx, w.sess, assembled, req, w.budgets)
	testutil.FailErr(t, "fit", err)
	body, err := w.provider.RenderRequest(ctx, fitted)
	testutil.FailErr(t, "render", err)
	var wire struct {
		Messages []json.RawMessage `json:"messages"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal(body, &wire))
	rows := make([]string, len(wire.Messages))
	for i, raw := range wire.Messages {
		rows[i] = string(raw)
	}
	return fitted.Messages, rows
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// A turn at the row bound of the live budget, with small bodies: nothing but the
// row count can move the start of history, and it must not move per request.
func TestRowBoundHoldsAcrossALongActiveTurn_AC_KV_03(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow = 196608
	normalized, err := compaction.NormalizeConfig(cfg)
	testutil.FailErr(t, "normalize", err)
	svc, mem, sess := stableTurnSession(t, normalized)
	seedTurn(t, mem, sess.ID, 200, "small")
	maxRows := svc.canonicalTailBounds(normalized).maxRows
	if maxRows >= 600 {
		t.Fatalf("row bound %d is out of the fixture's reach", maxRows)
	}
	starts := map[int64]bool{}
	for step := range 200 {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(5000+step, "small")...))
		loaded, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		starts[firstRetainedOrd(loaded)] = true
	}
	// One move per 32-row cycle over 400 rows.
	if len(starts) > 400/PrefixGridRowStep+1 {
		t.Fatalf("history start moved to %d positions over 400 rows", len(starts))
	}
}

// With an oversized body on record, a fit sizes body projection and the retained
// tail itself. They must leave the reserve too, or every append moves the cut.
func TestFitHoldsItsCutWhenBodiesBoundIt_AC_KV_04(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow, cfg.HardCeilingTokens, cfg.TargetTokens = 196608, 60000, 30000
	svc, mem, sess := stableTurnSession(t, cfg)
	svc.recallAvailable = func(context.Context, *api.Session) bool { return true }
	body := strings.Repeat("observation ", 125)
	seedTurn(t, mem, sess.ID, 200, body)
	testutil.FailErr(t, "append oversized body", mem.AppendMessages(t.Context(), sess.ID, turnExchange(4999, strings.Repeat("large observation ", 400))...))
	request := wireRequest{svc: svc, sess: sess, frame: strings.Repeat("Standing instructions. ", 400), budgets: fixedBudgets{window: cfg.ModelContextWindow},
		provider: openaicompat.New("fixture", "http://127.0.0.1:1", "fixture", []modelinfo.Entry{{ID: "fixture-model"}}).WithProfile(providerprofile.Fireworks())}
	var previous []string
	moved := 0
	const appends = 64
	for step := range appends {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(5000+step, body)...))
		_, wire := request.render(t)
		if previous != nil && commonPrefix(previous, wire) != len(previous) {
			moved++
		}
		previous = wire
	}
	t.Logf("moved %d of %d", moved, appends-1)
	if moved*4 > appends {
		t.Fatalf("cut moved on %d of %d appends", moved, appends-1)
	}
}
