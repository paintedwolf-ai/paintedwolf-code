package workerworkspace_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/workerworkspace"
)

func TestWorkerTouchLedgerRecordAndClear(t *testing.T) {
	ledger := workerworkspace.NewTouchLedger()
	ledger.RecordTouch("job-1", "internal/a.go")
	ledger.RecordTouch("job-1", "internal/b.go")
	paths := ledger.Paths("job-1")
	if len(paths) != 2 || paths[0] != "internal/a.go" {
		t.Fatalf("paths = %#v", paths)
	}
	ledger.ClearJob("job-1")
	if len(ledger.Paths("job-1")) != 0 {
		t.Fatal("expected cleared touches")
	}
}
