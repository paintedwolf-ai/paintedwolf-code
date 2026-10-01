package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/vocabulary"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowVocabularySchemaValidatesExport(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	entries := vocabulary.ExportCatalog()
	if len(entries) == 0 {
		t.Fatal("expected non-empty vocabulary export")
	}
	data, err := json.Marshal(map[string]any{"entries": entries})
	contractcheck.FailErr(t, "json.Marshal failed", err)

	// The live export must satisfy the committed JSON schema — not merely be
	// well-formed JSON. A new CatalogEntry field or enum value that the schema
	// forbids fails here.
	sch := compileSchemaBundle(t, "workflow_vocabulary.schema.json")
	validateJSONInstance(t, sch, data)

	// The committed catalog snapshot must also validate and stay in lockstep with
	// the live export.
	catalogPath := filepath.Join(root, "schemas", "workflow_vocabulary.json")
	catalogBytes, err := os.ReadFile(catalogPath)
	contractcheck.FailErr(t, "read committed catalog", err)
	validateJSONInstance(t, sch, catalogBytes)
	var committed struct {
		Entries []vocabulary.CatalogEntry `json:"entries"`
	}
	if err := json.Unmarshal(catalogBytes, &committed); err != nil {
		contractcheck.FailErr(t, "unmarshal committed catalog", err)
	}
	if len(committed.Entries) != len(entries) {
		t.Fatalf("catalog entries = %d export = %d", len(committed.Entries), len(entries))
	}
}
