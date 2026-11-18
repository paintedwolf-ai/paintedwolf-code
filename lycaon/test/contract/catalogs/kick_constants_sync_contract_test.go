package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestKickTemplatesHaveInformBindings is the reverse closure: every coordinator-*.md
// and worker child template has an inform Binding (builtin or workflow inject).
func TestKickTemplatesHaveInformBindings(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	kicksDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "guidance")
	reg := catalogfixture.LoadInformBindings(t)
	indexWorkflowInjects(t, reg)
	renders := map[string]struct{}{}
	for _, b := range reg.AllInform() {
		if b != nil && b.Render != "" {
			renders[b.Render] = struct{}{}
		}
	}
	for _, b := range reg.BindingsOn(catalogfixture.MustParseAnchor(t, "phase.entered")) {
		if b != nil && b.Render != "" {
			renders[b.Render] = struct{}{}
		}
	}
	entries, err := os.ReadDir(kicksDir)
	contractcheck.FailErr(t, "read kicks dir", err)
	var orphan []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(name, ".md")
		if _, ok := renders[stem]; !ok {
			orphan = append(orphan, name)
		}
	}
	contractcheck.FailViolations(t, "kick templates without inform Binding.render", orphan)
}
