package vocabulary

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExportCatalogMatchesCommittedFile(t *testing.T) {
	root := configlayout.FindModuleRoot()
	path := filepath.Join(root, "..", "schemas", "workflow_vocabulary.json")
	if os.Getenv("REGEN_VOCAB") == "1" {
		writeExportCatalog(t, path)
	}
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read file", err)
	var committed struct {
		Entries []CatalogEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &committed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	got := ExportCatalog()
	if !reflect.DeepEqual(committed.Entries, got) {
		t.Fatalf("export drift: committed %d entries, got %d — regenerate schemas/workflow_vocabulary.json", len(committed.Entries), len(got))
	}
}

func writeExportCatalog(t *testing.T, path string) {
	t.Helper()
	out := map[string]any{
		"$schema": "./workflow_vocabulary.schema.json",
		"entries": ExportCatalog(),
	}
	b, err := json.MarshalIndent(out, "", "  ")
	testutil.FailErr(t, "json.MarshalIndent failed", err)
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}
