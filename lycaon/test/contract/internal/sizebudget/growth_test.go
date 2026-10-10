package sizebudget

import "testing"

func TestGrowthDistinguishesDebtFromNewExcess(t *testing.T) {
	policy := Policy{Limits: map[string]Limit{"files": {Warn: 5, Limit: 10}}}
	for _, tc := range []struct {
		before, after int
		want          Kind
	}{
		{15, 15, LegacyDebt}, {15, 12, LegacyDebt}, {15, 16, OverLimit}, {0, 11, OverLimit},
	} {
		findings := EvaluateGrowth(policy, Measurements{"files": {"a": tc.before}}, Measurements{"files": {"a": tc.after}}, func(_, _ string) bool { return true })
		if len(findings) != 1 || findings[0].Kind != tc.want {
			t.Errorf("%d -> %d: got %v, want %s", tc.before, tc.after, findings, tc.want)
		}
	}
	policy.Exceptions = map[string]map[string]Exception{"files": {"a": {Cap: 12, Reason: "fixed table"}}}
	findings := EvaluateGrowth(policy, Measurements{"files": {"a": 15}}, Measurements{"files": {"a": 14}}, func(_, _ string) bool { return true })
	if findings[0].Kind != OverCap {
		t.Fatal("shrinking must not excuse exceeding an explicit cap")
	}
}

func TestGrowthWarningAndHardLimitBoundaries(t *testing.T) {
	policy := Policy{Limits: map[string]Limit{"files": {Warn: 10, Limit: 20}}}
	for _, tc := range []struct {
		measured int
		want     Kind
		fails    bool
	}{
		{10, "", false}, {11, OverWarn, false}, {20, OverWarn, false}, {21, OverLimit, true},
	} {
		findings := EvaluateGrowth(policy, Measurements{"files": {}}, Measurements{"files": {"new": tc.measured}}, func(_, _ string) bool { return true })
		if tc.want == "" {
			if len(findings) != 0 {
				t.Fatalf("measurement %d: unexpected finding %v", tc.measured, findings)
			}
			continue
		}
		if len(findings) != 1 || findings[0].Kind != tc.want || (len(Failures(findings)) > 0) != tc.fails {
			t.Fatalf("measurement %d: got %v, want %s (fails=%v)", tc.measured, findings, tc.want, tc.fails)
		}
	}
}
