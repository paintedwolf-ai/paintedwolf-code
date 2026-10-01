package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStoreBaselineDiagnosticUsesBootShapeAndPreservesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store := testdbfixture.OpenPath(t, path)
	testutil.FailErr(t, "close baseline store", store.Shutdown(t.Context()))
	var output bytes.Buffer
	testutil.FailErr(t, "inspect baseline store", writeStoreBaseline(t.Context(), path, &output))
	var result storeBaselineResult
	testutil.FailErr(t, "decode baseline result", json.Unmarshal(output.Bytes(), &result))
	if !result.Compatible || result.SchemaVersion != db.SchemaVersion || result.StoreSchemaVersion != db.SchemaVersion {
		t.Fatalf("fresh baseline result = %+v", result)
	}
	writer, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "open shape fixture", err)
	defer func() { _ = writer.Close() }()
	_, err = writer.ExecContext(t.Context(), "CREATE INDEX diagnostic_extra_index ON projects(id)")
	testutil.FailErr(t, "change shape without marker change", err)
	testutil.FailErr(t, "close shape fixture", writer.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused store bytes", err)
	output.Reset()
	testutil.FailErr(t, "inspect changed shape", writeStoreBaseline(t.Context(), path, &output))
	testutil.FailErr(t, "decode incompatible result", json.Unmarshal(output.Bytes(), &result))
	if result.Compatible || result.RecoveryReason != db.RecoveryReasonSchemaMismatch || result.StoreSchemaVersion != db.SchemaVersion {
		t.Fatalf("changed shape result = %+v", result)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read store after inspection", err)
	if !bytes.Equal(before, after) {
		t.Fatal("baseline inspection changed the refused store")
	}
}
