package app

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPerformanceGaugesCarryStoreWorkAndMaintenance(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = store.Close() })
	gauges := (&runtimeResources{db: store}).performanceGauges()
	for name := range db.ProcessWork().Counters() {
		if _, ok := gauges["store_"+name]; !ok {
			t.Fatalf("gauges lack store_%s", name)
		}
	}
	for name := range store.MaintenanceWork() {
		if _, ok := gauges["store_"+name]; !ok {
			t.Fatalf("gauges lack store_%s", name)
		}
	}
	if gauges["store_statements"] <= 0 {
		t.Fatalf("opening a store counted %d statements", gauges["store_statements"])
	}
	if (&runtimeResources{}).performanceGauges() != nil {
		t.Fatal("gauges without a store")
	}
}
