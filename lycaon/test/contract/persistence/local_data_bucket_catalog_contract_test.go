package contract_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestLocalDataBucketCatalogMatchesWire keeps the runtime clear catalog aligned
// with the OpenAPI vocab — localdata defines paths; pkg/api defines the wire enum.
func TestLocalDataBucketCatalogMatchesWire(t *testing.T) {
	t.Parallel()
	wireIDs := api.AllLocalDataBucketIdValues()
	hostIDs := localdata.Catalog()
	if len(wireIDs) != len(hostIDs) {
		t.Fatalf("wire=%d host=%d", len(wireIDs), len(hostIDs))
	}
	for i := range wireIDs {
		if string(wireIDs[i]) != hostIDs[i] {
			t.Fatalf("order mismatch at %d: wire=%q host=%q", i, wireIDs[i], hostIDs[i])
		}
	}
}
