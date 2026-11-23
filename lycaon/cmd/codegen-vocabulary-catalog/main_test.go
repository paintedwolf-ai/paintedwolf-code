package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vocabulary"
)

func TestRenderMatchesTheCommittedSnapshot(t *testing.T) {
	root := configlayout.FindModuleRoot()
	path := filepath.Join(root, "..", "schemas", "workflow_vocabulary.json")
	committed, err := os.ReadFile(path)
	testutil.FailErr(t, "read file", err)
	if got := render(); string(committed) != got {
		t.Fatalf("render drift: committed %d bytes, rendered %d — run ./task codegen:vocabulary-catalog", len(committed), len(got))
	}
}

func TestRenderShapesTheVocabularyDocument(t *testing.T) {
	var doc struct {
		Schema  string                    `json:"$schema"`
		Entries []vocabulary.CatalogEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(render()), &doc); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if doc.Schema != "./workflow_vocabulary.schema.json" {
		t.Errorf("$schema %q", doc.Schema)
	}
	if len(doc.Entries) == 0 {
		t.Fatal("no catalog entries")
	}
	if !reflect.DeepEqual(doc.Entries, vocabulary.ExportCatalog()) {
		t.Fatalf("rendered entries diverge from the catalog: %d vs %d", len(doc.Entries), len(vocabulary.ExportCatalog()))
	}
}

func TestRunGeneratesAndChecksTheSnapshot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "workflow_vocabulary.json")
	if err := run([]string{"--out", out}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	generated, err := os.ReadFile(out)
	testutil.FailErr(t, "read generated snapshot", err)
	if string(generated) != render() {
		t.Fatalf("generated snapshot diverges from render(): %d vs %d bytes", len(generated), len(render()))
	}
	if err := run([]string{"--out", out, "--check"}); err != nil {
		t.Fatalf("check fresh snapshot: %v", err)
	}
	if err := os.WriteFile(out, []byte("stale"), 0o600); err != nil {
		t.Fatalf("make generated output stale: %v", err)
	}
	err = run([]string{"--out", out, "--check"})
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("stale output error = %v", err)
	}
}

func TestRunRequiresTheOutputPath(t *testing.T) {
	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "--out is required") {
		t.Fatalf("missing output path error = %v", err)
	}
	if err := run([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag succeeded")
	}
}
