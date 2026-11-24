package vocabulary_test

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/vocabulary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExportCatalogMatchesCommittedFile(t *testing.T) {
	root := configlayout.FindModuleRoot()
	path := filepath.Join(root, "..", "schemas", "workflow_vocabulary.json")
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read file", err)
	var committed struct {
		Entries []vocabulary.CatalogEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &committed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	got := vocabulary.ExportCatalog()
	if !reflect.DeepEqual(committed.Entries, got) {
		t.Fatalf("export drift: committed %d entries, got %d — run ./task codegen:vocabulary-catalog", len(committed.Entries), len(got))
	}
}
