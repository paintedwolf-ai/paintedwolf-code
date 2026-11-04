package db

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleaseSchemaLockMatchesHost(t *testing.T) {
	raw, err := os.ReadFile("schema_version_lock.json")
	testutil.FailErr(t, "read release schema lock", err)
	var lock struct {
		Version int `json:"schema_version"`
	}
	testutil.FailErr(t, "decode release schema lock", json.Unmarshal(raw, &lock))
	if lock.Version != SchemaVersion {
		t.Fatalf("release schema lock = %d, host schema = %d", lock.Version, SchemaVersion)
	}
}

// Schema changes join the one unpublished revision; revisions that no published
// build wrote never accumulate as upgrade sources.
func TestSchemaVersionAdvancesOnceFromShippedRevisions(t *testing.T) {
	var released []migrations.Baseline
	testutil.FailErr(t, "decode released schemas", json.Unmarshal(releasedBaselinesJSON, &released))
	newest := 0
	for _, baseline := range released {
		newest = max(newest, baseline.Revision)
	}
	if SchemaVersion > newest+1 {
		t.Fatalf("schema revision %d is more than one step past shipped revision %d", SchemaVersion, newest)
	}
}
