package kick

import "testing"

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
