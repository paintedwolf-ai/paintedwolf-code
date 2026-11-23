package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/vocabulary"
)

func TestWorkflowVocabularyCoreLayer(t *testing.T) {
	t.Parallel()
	for _, row := range vocabulary.ExportCatalog() {
		if row.Status != "shipped" {
			continue
		}
		if row.Domain != "—" && row.Domain != "" {
			continue
		}
		if row.Layer != "core" {
			t.Fatalf("shipped core id %q has layer %q", row.ID, row.Layer)
		}
	}
}
