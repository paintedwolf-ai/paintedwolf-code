package history

import (
	"encoding/json"
	"fmt"
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

// A turn longer than the row bound, with bodies of very different sizes, under a
// published view. One replayed request must stay within budget, keep its exchanges
// whole, and extend the previous request's wire messages between explained cuts.
func TestLongTurnThroughLoadPublishFitAndWire_AC_KV_06_AC_KV_07_AC_KV_09(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow, cfg.HardCeilingTokens, cfg.TargetTokens = 196608, 60000, 30000
	svc, mem, sess := stableTurnSession(t, cfg)
	svc.SetCompactor(compaction.NewSimpleCompactor(cfg, &scriptedProvider{script: []func() (string, error){answerShort}}))
	canonical := seedTurn(t, mem, sess.ID, 40, strings.Repeat("early observation ", 30))
	publishCoverage(t, mem, sess.ID, canonical, canonical[30].Ord, 1, cfg.ModelContextWindow)

	request := wireRequest{svc: svc, sess: sess, frame: strings.Repeat("Standing instructions. ", 400), budgets: fixedBudgets{window: cfg.ModelContextWindow},
		provider: openaicompat.New("fixture", "http://127.0.0.1:1", "fixture", []modelinfo.Entry{{ID: "fixture-model"}}).WithProfile(providerprofile.Fireworks())}
	sizes := []int{40, 900, 120, 6000, 300, 2600, 80, 12000, 500, 1500, 60, 3500}
	var previous []string
	held, moved, longest := 0, 0, 0
	const appends = 480
	for step := range appends {
		body := strings.Repeat("observation ", sizes[step%len(sizes)]/12+1)
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(5000+step, fmt.Sprintf("%d %s", step, body))...))
		rows, wire := request.render(t)
		if input := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(rows)); input > cfg.HardCeilingTokens {
			t.Fatalf("step %d: %d tokens over the %d ceiling", step, input, cfg.HardCeilingTokens)
		}
		if got := countID(rows, "opener"); got != 1 {
			t.Fatalf("step %d: opener appears %d times", step, got)
		}
		assertPairedExchanges(t, rows)
		if len(wire) != len(rows) {
			t.Fatalf("step %d: adapter dropped rows, %d of %d reached the wire", step, len(wire), len(rows))
		}
		longest = max(longest, len(rows))
		if previous != nil {
			// The final row of the previous request is the row before this append.
			if commonPrefix(previous, wire) == len(previous) {
				held++
			} else {
				moved++
			}
		}
		previous = wire
	}
	t.Logf("held %d, moved %d, longest %d rows", held, moved, longest)
	if moved == 0 {
		t.Fatalf("the fixture never reached its ceiling, longest %d rows", longest)
	}
	if moved*8 > held+moved {
		t.Fatalf("prefix moved on %d of %d requests", moved, held+moved)
	}
}

// The same turn at the row bound of the live budget, with small bodies: nothing but
// the row count can move the start of history, and it must not move per request.
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
