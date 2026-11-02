package db

import (
	"encoding/json"
	"os"
	"testing"

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
