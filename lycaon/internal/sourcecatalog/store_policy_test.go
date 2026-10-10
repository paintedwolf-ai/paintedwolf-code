package sourcecatalog

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestShippedTreeStorePolicy(t *testing.T) {
	raw, err := config.Read(config.StorageSourceCatalog)
	testutil.FailErr(t, "read shipped source retention", err)
	policy, err := decodeTreeStorePolicy(raw)
	testutil.FailErr(t, "decode shipped source retention", err)
	if policy.retention != 14*24*time.Hour || policy.maxBytes != 1610612736 || policy.vacuumPages != 2048 {
		t.Fatalf("unexpected shipped retention policy: %+v", policy)
	}
	if got := defaultTreeStorePolicy(); got != policy {
		t.Fatalf("loaded policy=%+v, want %+v", got, policy)
	}
}

func TestTreeStorePolicyRejectsInvalidBudgets(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed": "source_catalog: [",
		"missing":   "source_catalog: {}",
		"age":       "source_catalog: {retention_days: -1, max_total_bytes: 1024, vacuum_pages: 10}",
		"bytes":     "source_catalog: {retention_days: 1, max_total_bytes: 0, vacuum_pages: 10}",
		"vacuum":    "source_catalog: {retention_days: 1, max_total_bytes: 1024, vacuum_pages: -10}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTreeStorePolicy([]byte(raw)); err == nil {
				t.Fatal("invalid retention policy was accepted")
			}
		})
	}
}
