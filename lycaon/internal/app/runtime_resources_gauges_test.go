package app

import (
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

func TestPerformanceGaugesCarryStoreWorkAndMaintenance(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
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
