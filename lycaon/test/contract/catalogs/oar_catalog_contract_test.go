package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestOARHintCoverageUsesCatalogAnchors(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	hintsAt := extpacks.Bundled(config.PlatformPolicy)
	schemaDir := filepath.Join(root, "schemas")
	loader, err := oar.NewLoader(schemaDir)
	contractcheck.FailErr(t, "oar loader", err)
	rs, err := loader.LoadDir(hintsAt)
	contractcheck.FailErr(t, "load OAR rules", err)
	cfg, err := guidance.LoadHintConfig(hintsAt)
	contractcheck.FailErr(t, "load hint registry", err)

	var missing []string
	var noRule []string
	var unknownAnchors []string
	for code, entry := range cfg.HintCodes {
		emit := strings.TrimSpace(entry.Emit)
		if !strings.HasPrefix(emit, "guard:") && !strings.HasPrefix(emit, "rule:") {
			continue
		}
		anchor := strings.TrimSpace(entry.Anchor)
		kind := strings.TrimSpace(entry.Kind)
		if anchor == "" || kind == "" {
			missing = append(missing, code)
			continue
		}
		if _, ok := rs.Get(code); !ok {
			noRule = append(noRule, code)
		}
		resolved, err := oar.InstalledEngineDeclaration().ResolveAnchor(anchor)
		if err != nil {
			unknownAnchors = append(unknownAnchors, code+"="+anchor)
			continue
		}
		if !anchorcatalog.Has(resolved) {
			unknownAnchors = append(unknownAnchors, code+"="+anchor)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("hints missing anchor or kind:\n%s", strings.Join(missing, "\n"))
	}
	if len(noRule) > 0 {
		t.Fatalf("hints with anchor and kind but not loaded as OAR rules:\n%s", strings.Join(noRule, "\n"))
	}
	if len(unknownAnchors) > 0 {
		t.Fatalf("unknown OAR anchors:\n%s", strings.Join(unknownAnchors, "\n"))
	}
}
