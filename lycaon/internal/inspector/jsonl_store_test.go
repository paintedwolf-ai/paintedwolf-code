package inspector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvidenceStoreReadsLargeRecordsAndRejectsDamagedTail(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore("evidence")
	record := evidence.Record{GateType: "verify", Slot: "tests", RunID: "run", GateVerdict: "passed", Summary: strings.Repeat("x", 128*1024)}
	testutil.FailErr(t, "write evidence", store.Append(t.Context(), root, record))
	records, err := store.ReadAll(t.Context(), root, "run", "tests", evidence.GateTypeVerify)
	testutil.FailErr(t, "read large evidence", err)
	if len(records) != 1 || records[0].Summary != record.Summary {
		t.Fatal("evidence was truncated")
	}
	path := filepath.Join(root, EvidencePath("evidence", "run", "tests", evidence.GateTypeVerify))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	testutil.FailErr(t, "open evidence tail", err)
	_, err = file.WriteString("{broken\n")
	testutil.FailErr(t, "damage evidence tail", err)
	testutil.FailErr(t, "close evidence tail", file.Close())
	if _, err := store.ReadAll(t.Context(), root, "run", "tests", evidence.GateTypeVerify); err == nil {
		t.Fatal("damaged evidence was silently omitted")
	}
}
