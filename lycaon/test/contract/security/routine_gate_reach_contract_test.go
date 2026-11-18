package contract

import "testing"

func TestRoutineGateReachesVulnAndDriftWithoutActiveFuzzing(t *testing.T) {
	t.Parallel()
	doc := loadTaskfile(t)

	for _, want := range []string{"db:sqlc:check", "lint:vuln"} {
		if !doc.reaches("check", want) {
			t.Errorf("check no longer reaches %s", want)
		}
	}
	if doc.reaches("check", "test:fuzz") {
		t.Error("active fuzz exploration belongs in nightly and release verification")
	}
}
