package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestInformCatalogBound(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	catalogIDs, _ := loadCatalogAnchors(t, root)
	reg := catalogfixture.LoadInformBindings(t)
	var missing []string
	for id := range catalogIDs {
		if !strings.HasPrefix(id, "inject.") && id != "board.changed" {
			continue
		}
		if id == "loop.wake" {
			continue
		}
		// Only inform-content assembly injects / board — skip non-inject catalog rows.
		planesOK := id == "board.changed" || strings.HasPrefix(id, "inject.")
		if !planesOK {
			continue
		}
		b, err := reg.ResolveInform(anchor.ID(id), anchor.MatchContext{})
		if err != nil || b == nil || strings.TrimSpace(b.Render) == "" {
			// Try surface-scoped selectors used by Bindings.
			for _, surface := range []string{"coordinator", "worker"} {
				b, err = reg.ResolveInform(anchor.ID(id), anchor.MatchContext{Surface: surface})
				if err == nil && b != nil && strings.TrimSpace(b.Render) != "" {
					break
				}
			}
		}
		if b == nil || strings.TrimSpace(b.Render) == "" {
			missing = append(missing, id)
		}
	}
	contractcheck.FailViolations(t, "inform-content catalog Anchors missing Binding (excl. loop.wake)", missing)
}
