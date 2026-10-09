package maintainability

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func TestTrackingIncludesUntouchedAndExceptedDebtAboveWarning(t *testing.T) {
	policy := sizebudget.Policy{Limits: map[string]sizebudget.Limit{
		"source_files": {Warn: 10, Limit: 20}, "go_receiver_lines": {Warn: 10, Limit: 20},
	}, Exceptions: map[string]map[string]sizebudget.Exception{
		"source_files": {"excepted.go": {Cap: 40, Reason: "composition root"}},
	}}
	inv := &inventory{measured: newMeasurements(), sources: map[string][]string{
		"pkg/p.Server": {"pkg/z.go", "pkg/a.go"},
	}}
	inv.measured["source_files"] = map[string]int{"at-warn.go": 10, "above.go": 11, "excepted.go": 35, "legacy.go": 50}
	inv.measured["go_receiver_lines"]["pkg/p.Server"] = 12
	report := trackingReport(policy, inv, func(_, id string) bool { return id == "above.go" })
	if !report.Complete || report.SchemaVersion != 1 || len(report.Artifacts) != 4 {
		t.Fatalf("incomplete tracking report: %+v", report)
	}
	if got := report.Artifacts[0]; got.ID != "pkg/p.Server" || !reflect.DeepEqual(got.Sources, []string{"pkg/a.go", "pkg/z.go"}) {
		t.Fatalf("tracking order and sources: %+v", got)
	}
	if got := report.Artifacts[2]; got.ID != "excepted.go" || got.EffectiveCap != 40 || got.ExceptionReason != "composition root" {
		t.Fatalf("exception tracking: %+v", got)
	}
	for _, row := range report.Artifacts {
		if row.Touched != (row.ID == "above.go") {
			t.Fatalf("tracking touch status: %+v", row)
		}
		if row.Warn != 10 || row.Limit != 20 || row.Measured <= row.Warn {
			t.Fatalf("invalid tracking bounds: %+v", row)
		}
	}
}

func TestTrackingEmptySnapshotCanResolveExistingIssues(t *testing.T) {
	inv := &inventory{measured: newMeasurements()}
	report := trackingReport(sizebudget.Policy{}, inv, func(_, _ string) bool { return false })
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encode tracking report: %v", err)
	}
	if string(body) != `{"schema_version":1,"complete":true,"artifacts":[]}` {
		t.Fatalf("empty snapshot is not complete: %s", body)
	}
}
