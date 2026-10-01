package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestCoordinatorKickTemplateClosure locks Binding.render ↔ coordinator-*.md:
// kick text lives in the templates, never in Go consts.
func TestCoordinatorKickTemplateClosure(t *testing.T) {
	t.Parallel()
	catalogfixture.AssertNoKickConstFiles(t)
	reg := catalogfixture.LoadInformBindings(t)

	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() || !strings.HasPrefix(b.Render, "coordinator-") {
			continue
		}
		at, err := catalogfixture.StockGuidancePath(b.Render)
		if err != nil {
			t.Errorf("Binding %s missing template %s.md under stock guidance/: %v", b.On, b.Render, err)
			continue
		}
		data, err := at.Read()
		if err != nil {
			t.Errorf("Binding %s read template %s: %v", b.On, at, err)
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("kick template %s is empty", at)
		}
	}
}

// TestWorkerKickTemplateExists checks worker-child Binding templates exist.
func TestWorkerKickTemplateExists(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	kickDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "guidance")
	for _, name := range []string{
		"worker-leg-started.md", "worker-task-started.md", "worker-closeout.md",
		"worker-cancel-closeout.md", "worker-iterations-low.md", "worker-summary-trim.md",
		"worker-citation-grounding.md", "worker-budget-raised.md",
	} {
		path := filepath.Join(kickDir, name)
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read file", err)
		if strings.TrimSpace(string(data)) == "" {
			t.Fatalf("%s kick template is empty", name)
		}
	}
	reg := catalogfixture.LoadInformBindings(t)
	for _, id := range []string{
		"worker.task.started", "worker.leg.started", "worker.closeout",
		"worker.cancel.closeout", "worker.iterations.low", "worker.summary.trim",
		"worker.citation.grounding", "worker.budget.raised",
	} {
		b, ok := reg.Inform(catalogfixture.MustParseAnchor(t, id))
		if !ok || b == nil || b.Render == "" {
			t.Errorf("missing inform Binding for %s", id)
		}
	}
}
