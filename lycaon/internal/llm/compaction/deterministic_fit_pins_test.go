package compaction

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// charterRow supplies pinned worker instructions.
func charterRow() ContextMessage {
	return ContextMessage{
		ID:            "charter",
		Role:          string(api.MessageRoleUser),
		Content:       guidance.MarkerWorkerTaskPreamble + "\nTask mode: read\nSuggested paths: lycaon/internal/webresearch/**",
		ContextPinned: true,
	}
}

func withCharter(rows []ContextMessage) []ContextMessage {
	return append([]ContextMessage{charterRow()}, rows...)
}

func hasCharter(msgs []ContextMessage) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, guidance.MarkerWorkerTaskPreamble) {
			return true
		}
	}
	return false
}

func countTrimNotices(msgs []ContextMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Content == guidance.ContextTrimNotice {
			n++
		}
	}
	return n
}

// Pinned instructions survive budgets smaller than the protected tail.
func TestDeterministicFitNeverEvictsPinnedCharter(t *testing.T) {
	msgs := withCharter(fitRows(400))
	total := EstimateMessagesTokens(msgs)
	for _, frac := range []int{95, 75, 50, 25, 10, 2, 1} {
		budget := total * frac / 100
		out := DeterministicFit(fitCfg(), msgs, budget)
		if !hasCharter(out) {
			t.Fatalf("frac %d%% (budget %d): charter evicted from %d retained rows",
				frac, budget, len(out))
		}
	}
}

// The charter stays ahead of the retained evidence.
func TestDeterministicFitKeepsCharterAheadOfRetainedWindow(t *testing.T) {
	msgs := withCharter(fitRows(300))
	budget := EstimateMessagesTokens(msgs) / 4
	out := DeterministicFit(fitCfg(), msgs, budget)
	if len(out) == 0 || !strings.Contains(out[0].Content, guidance.MarkerWorkerTaskPreamble) {
		t.Fatalf("charter is not the first retained row; got role=%q content=%.60q",
			out[0].Role, out[0].Content)
	}
}

// Trimming produces one notice between the charter and retained evidence.
func TestDeterministicFitAnnouncesTrim(t *testing.T) {
	msgs := withCharter(fitRows(300))
	budget := EstimateMessagesTokens(msgs) / 4
	out := DeterministicFit(fitCfg(), msgs, budget)
	if got := countTrimNotices(out); got != 1 {
		t.Fatalf("want exactly one trim notice, got %d", got)
	}

	noticeIdx, windowIdx := -1, -1
	for i, m := range out {
		if m.Content == guidance.ContextTrimNotice && noticeIdx < 0 {
			noticeIdx = i
		}
		if noticeIdx >= 0 && i > noticeIdx && m.Content != guidance.ContextTrimNotice {
			windowIdx = i
			break
		}
	}
	if noticeIdx <= 0 || windowIdx < 0 {
		t.Fatalf("trim notice at %d, retained window at %d in %d rows", noticeIdx, windowIdx, len(out))
	}
}

// A transcript that fits is left exactly alone: no notice, no reordering.
func TestDeterministicFitSilentWhenNothingDropped(t *testing.T) {
	msgs := withCharter(fitRows(10))
	out := DeterministicFit(fitCfg(), msgs, EstimateMessagesTokens(msgs)*2)
	if got := countTrimNotices(out); got != 0 {
		t.Fatalf("announced a trim that did not happen (%d notices)", got)
	}
	if len(out) != len(msgs) {
		t.Fatalf("dropped %d rows while under budget", len(msgs)-len(out))
	}
}

// Repeated fitting retains one notice.
func TestDeterministicFitRefreshesRatherThanStacksNotices(t *testing.T) {
	msgs := withCharter(fitRows(300))
	first := DeterministicFit(fitCfg(), msgs, EstimateMessagesTokens(msgs)/4)
	second := DeterministicFit(fitCfg(), first, EstimateMessagesTokens(first)/2)
	if got := countTrimNotices(second); got != 1 {
		t.Fatalf("want one trim notice after a second pass, got %d", got)
	}
	if !hasCharter(second) {
		t.Fatal("charter lost on the second fit pass")
	}
}

// A session with a charter still trims to a usable size.
func TestDeterministicFitStaysUnderBudgetWithPins(t *testing.T) {
	msgs := withCharter(fitRows(300))
	total := EstimateMessagesTokens(msgs)
	for _, frac := range []int{75, 50, 25} {
		budget := total * frac / 100
		out := DeterministicFit(fitCfg(), msgs, budget)
		if got := EstimateMessagesTokens(out); got > budget {
			t.Fatalf("frac %d%%: %d tokens over budget %d", frac, got, budget)
		}
	}
}

func TestDeterministicFitPreservesQuotedTrimNotice(t *testing.T) {
	for _, role := range []string{"user", "tool", "assistant"} {
		t.Run(role, func(t *testing.T) {
			quoted := ContextMessage{ID: "quoted-notice", Role: role, Content: guidance.ContextTrimNotice}
			out := DeterministicFit(fitCfg(), []ContextMessage{quoted}, 10000)
			if len(out) != 1 || out[0].ID != quoted.ID || out[0].Role != role {
				t.Fatalf("quoted notice was replaced: %+v", out)
			}
		})
	}
}

func TestDeterministicFitReplacesNoticeByIdentity(t *testing.T) {
	notice := contextTrimNotice()
	notice.Content = "Host trim notice from an earlier fitting pass."
	out := DeterministicFit(fitCfg(), []ContextMessage{charterRow(), notice}, 10000)
	if len(out) != 2 || out[1].ID != contextTrimNoticeID || out[1].Content != guidance.ContextTrimNotice {
		t.Fatalf("host notice was not refreshed: %+v", out)
	}
}
