package kick

import (
	"testing"
	"time"
)

func TestRoundNoticeSpendProjection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		facts     WorkerRoundNoticeFacts
		underCent bool
		usd       string
	}{
		{"unpriced", WorkerRoundNoticeFacts{SpendNanoUSD: 50_000_000}, false, ""},
		{"free", WorkerRoundNoticeFacts{SpendPriced: true}, false, ""},
		{"under a cent", WorkerRoundNoticeFacts{SpendPriced: true, SpendNanoUSD: nanoUSDPerCent - 1}, true, ""},
		{"a cent", WorkerRoundNoticeFacts{SpendPriced: true, SpendNanoUSD: nanoUSDPerCent}, false, "0.01"},
		{"dollars", WorkerRoundNoticeFacts{SpendPriced: true, SpendNanoUSD: 1_234_000_000}, false, "1.23"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{}
			tc.facts.renderData(data)
			under, _ := data["spend_under_cent"].(bool)
			usd, _ := data["estimated_spend_usd"].(string)
			if under != tc.underCent || usd != tc.usd {
				t.Fatalf("spend_under_cent=%v estimated_spend_usd=%q, want %v %q", under, usd, tc.underCent, tc.usd)
			}
		})
	}
}

func TestQuietRoundNoticeNamesItsBatchAndLatestMessage(t *testing.T) {
	data := map[string]any{}
	WorkerRoundNoticeFacts{
		Kind: RoundNoticeQuiet, InFlight: RoundInFlightTools, ToolCalls: 3, ToolsDone: 1,
		Tools: []string{"command"}, ToolsFor: 7*time.Minute + 30*time.Second, LatestMessageSeq: 4,
	}.renderData(data)
	if tools, _ := data["tools"].([]string); len(tools) != 1 || tools[0] != "command" {
		t.Fatalf("tools = %v", data["tools"])
	}
	if data["tools_minutes"] != 7 || data["latest_message_seq"] != int64(4) {
		t.Fatalf("tools_minutes=%v latest_message_seq=%v", data["tools_minutes"], data["latest_message_seq"])
	}
}
