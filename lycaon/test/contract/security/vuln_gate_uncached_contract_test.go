package contract

import "testing"

// Vulnerability checks cannot use repository-only freshness fingerprints.
func TestVulnGateDeclaresNoFingerprint(t *testing.T) {
	t.Parallel()
	doc := loadTaskfile(t)

	task, ok := doc.Tasks["lint:vuln"]
	if !ok {
		t.Fatal("Taskfile.yml no longer defines lint:vuln")
	}
	if len(task.Sources) > 0 {
		t.Errorf("lint:vuln declares sources %v; its input is the vulnerability database, "+
			"which changes without any file here changing", task.Sources)
	}
	if len(task.Generates) > 0 {
		t.Errorf("lint:vuln declares generates %v; it produces no artifact", task.Generates)
	}
	if len(task.Status) > 0 {
		t.Errorf("lint:vuln declares status %v; same skip hazard as sources", task.Status)
	}
}

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
