package compaction

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Each row costs ~25 tokens, so budgets translate cleanly into row counts.
func fitRows(n int) []ContextMessage {
	out := make([]ContextMessage, n)
	for i := range out {
		out[i] = ContextMessage{
			Role:    string(api.MessageRoleAssistant),
			Content: strings.Repeat("x", 100),
		}
	}
	return out
}

func fitCfg() CompactionConfig {
	return CompactionConfig{KeepRecentMessages: 16}
}

// fitDistinctBody gives every row the same token cost with different bytes, so a
// moved retained window is visible while budgets stay predictable.
func fitDistinctBody(i int) string {
	tag := strings.Repeat("x", 90)
	return tag + strings.Repeat("y", 10-len(strconv.Itoa(i))) + strconv.Itoa(i)
}

// Small overflow drops one stable block without collapsing to the floor.
func TestDeterministicFitDropsOnlyWhatIsNeeded(t *testing.T) {
	msgs := fitRows(200)
	total := EstimateMessagesTokens(msgs)
	perRow := EstimateMessagesTokens(msgs[:1])

	// Budget that requires shedding roughly ten rows.
	budget := total - perRow*10
	out := DeterministicFit(fitCfg(), msgs, budget)

	if got := EstimateMessagesTokens(out); got > budget {
		t.Fatalf("fit returned %d tokens over budget %d", got, budget)
	}
	if len(out) == len(msgs) {
		t.Fatal("fit dropped nothing while over budget")
	}
	// At most one block beyond what a minimal fit would have shed.
	if minKeep := len(msgs) - 10 - fitDropBlockMessages; len(out) < minKeep {
		t.Fatalf("kept %d of %d rows for a ten-row overflow; want >= %d "+
			"(one block past minimal)", len(out), len(msgs), minKeep)
	}
	// Retain well above the floor.
	if len(out) <= fitCfg().KeepRecentMessages*4 {
		t.Fatalf("kept only %d rows — that is the pre-graduated collapse", len(out))
	}
}

// The graduated path must still keep the result under budget at every scale, and
// must never dip below the KeepRecentMessages floor while doing it.
func TestDeterministicFitHonorsBudgetAndFloorAcrossScales(t *testing.T) {
	msgs := fitRows(300)
	total := EstimateMessagesTokens(msgs)
	for _, frac := range []int{95, 75, 50, 25, 10, 2} {
		budget := total * frac / 100
		out := DeterministicFit(fitCfg(), msgs, budget)
		got := EstimateMessagesTokens(out)
		retained := len(out) - countTrimNotices(out)
		if got > budget && retained > fitCfg().KeepRecentMessages {
			t.Fatalf("frac %d%%: %d tokens over budget %d with %d rows still kept",
				frac, got, budget, len(out))
		}
		if retained < fitCfg().KeepRecentMessages {
			t.Fatalf("frac %d%%: retained %d rows below the floor", frac, retained)
		}
		if len(out) > len(msgs) {
			t.Fatalf("frac %d%%: fit grew the history to %d rows", frac, len(out))
		}
	}
}

// The retained window must not move as the transcript grows turn by turn. The
// provider caches everything after the system block, so a drop point that advances
// every turn re-sends the whole history uncached.
func TestDeterministicFitKeepsRetainedWindowStableWhileGrowing(t *testing.T) {
	cfg := fitCfg()
	msgs := fitRows(200)
	perRow := EstimateMessagesTokens(msgs[:1])
	budget := EstimateMessagesTokens(msgs) - perRow*20

	firstRetained := func(out []ContextMessage) string {
		if len(out) == 0 {
			return ""
		}
		return out[0].Content
	}

	// Give each row distinct content so a moved window is detectable.
	for i := range msgs {
		msgs[i].Content = fitDistinctBody(i)
	}

	base := DeterministicFit(cfg, msgs, budget)
	baseHead := firstRetained(base)
	moves := 0
	grown := msgs
	for turn := 0; turn < 20; turn++ {
		// Two more rows per turn, the usual assistant + tool pair.
		grown = append(grown, ContextMessage{
			Role: string(api.MessageRoleAssistant), Content: fitDistinctBody(1000 + turn*2),
		}, ContextMessage{
			Role: string(api.MessageRoleTool), Content: fitDistinctBody(1001 + turn*2),
		})
		got := firstRetained(DeterministicFit(cfg, grown, budget))
		if got != baseHead {
			moves++
			baseHead = got
		}
	}
	// Block quantization keeps re-anchors rare across gradual growth.
	if moves > 2 {
		t.Fatalf("retained window moved %d times over 20 turns of growth; "+
			"each move re-sends the history uncached", moves)
	}
}

// Monotonicity: a tighter budget never keeps more rows than a looser one.
func TestDeterministicFitMonotonicInBudget(t *testing.T) {
	msgs := fitRows(150)
	total := EstimateMessagesTokens(msgs)
	prev := len(msgs) + 1
	for frac := 100; frac >= 10; frac -= 10 {
		out := DeterministicFit(fitCfg(), msgs, total*frac/100)
		if len(out) > prev {
			t.Fatalf("frac %d%% kept %d rows, more than the looser budget's %d", frac, len(out), prev)
		}
		prev = len(out)
	}
}

// A leading compaction checkpoint pair survives the fit — it carries the
// continuation record the rest of the history was summarized into.
func TestDeterministicFitPreservesCheckpointHead(t *testing.T) {
	msgs := append([]ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "checkpoint", CompactionCheckpoint: true},
		{Role: string(api.MessageRoleAssistant), Content: "summary of prior work"},
	}, fitRows(100)...)

	out := DeterministicFit(fitCfg(), msgs, EstimateMessagesTokens(msgs)/4)
	if len(out) < 2 {
		t.Fatalf("fit returned %d rows", len(out))
	}
	if !out[0].CompactionCheckpoint {
		t.Fatal("checkpoint row was dropped")
	}
	if out[1].Content != "summary of prior work" {
		t.Fatalf("summary row was dropped, got %q", out[1].Content)
	}
}

// An already-fitting history is returned untouched.
func TestDeterministicFitNoopWhenUnderBudget(t *testing.T) {
	msgs := fitRows(20)
	out := DeterministicFit(fitCfg(), msgs, EstimateMessagesTokens(msgs)+1000)
	if len(out) != len(msgs) {
		t.Fatalf("kept %d of %d rows while under budget", len(out), len(msgs))
	}
}

// Orphaned leading tool rows are stripped: a tool result whose assistant
// tool_call was dropped has nothing to answer, and providers reject that history.
func TestDeterministicFitStripsLeadingToolRows(t *testing.T) {
	msgs := fitRows(100)
	for i := 40; i < 45; i++ {
		msgs[i].Role = string(api.MessageRoleTool)
	}
	out := DeterministicFit(fitCfg(), msgs, EstimateMessagesTokens(msgs)/2)
	if len(out) > 0 && out[0].Role == string(api.MessageRoleTool) {
		t.Fatal("fit left a leading tool row")
	}
}

// The floor is a floor, not a target: an impossible budget lands at
// KeepRecentMessages rather than emptying the history.
func TestDeterministicFitFloorsAtKeepRecent(t *testing.T) {
	msgs := fitRows(200)
	out := DeterministicFit(fitCfg(), msgs, 1)
	if len(out) == 0 {
		t.Fatal("fit emptied the history")
	}
	if retained := len(out) - countTrimNotices(out); retained != fitCfg().KeepRecentMessages {
		t.Fatalf("impossible budget kept %d rows, want %d", retained, fitCfg().KeepRecentMessages)
	}
}
