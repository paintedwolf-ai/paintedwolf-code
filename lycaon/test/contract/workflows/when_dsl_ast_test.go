package contract

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The bundled hints load with their when clauses, each of which the loader's
// condition check accepts; a when built on a forbidden condition id is refused.
func TestHintWhenExpressionsValidate(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "oar loader", err)
	rs, err := loader.LoadDir(extpacks.Bundled(config.PlatformPolicy))
	contractcheck.FailErr(t, "load OAR rules", err)
	withWhen := 0
	for _, r := range rs.All() {
		if r.When == "" {
			continue
		}
		withWhen++
		if err := loader.ValidateCondition(r.When); err != nil {
			t.Errorf("rule %q: loaded when %q refused: %v", r.ID, r.When, err)
		}
	}
	if withWhen == 0 {
		t.Fatalf("bundled policy loaded %d rules and none carries a when", rs.Len())
	}
	for _, id := range []string{"mode_is", "mode_unresolved", "den_status", "stage_plan_complete", "user_input_clarify"} {
		if !conditions.IsForbidden(id) {
			t.Fatalf("%q is not a forbidden condition id", id)
		}
		var unknown *oarcore.TypeError
		if err := loader.ValidateCondition(id); !errors.As(err, &unknown) || unknown.Clause != "OAR-EXPR-2" {
			t.Errorf("when %q: error = %v, want the unknown-identifier refusal OAR-EXPR-2", id, err)
		}
	}
}
