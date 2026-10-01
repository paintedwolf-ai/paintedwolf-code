package workflowvalidate_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestValidateCatalogBundledPasses(t *testing.T) {
	root := configlayout.FindModuleRoot()
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModeBundled,
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s [%s] %s | %s", d.Field, d.Code, d.Message, d.Replacement)
		}
		t.Fatalf("bundled catalog has %d diagnostics", len(diags))
	}
}

func TestCatalogSourcesBijection(t *testing.T) {
	root := configlayout.FindModuleRoot()
	reg, sources, err := workflowvalidate.CatalogSources(workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModeBundled,
	})
	testutil.FailErr(t, "CatalogSources", err)
	if len(sources) == 0 {
		t.Fatal("expected bundled sources")
	}
	regKeys := map[string]struct{}{}
	for k := range reg.All() {
		regKeys[k] = struct{}{}
	}
	srcKeys := map[string]struct{}{}
	for _, s := range sources {
		srcKeys[s.Key] = struct{}{}
	}
	if len(regKeys) != len(srcKeys) {
		t.Fatalf("registry=%d sources=%d", len(regKeys), len(srcKeys))
	}
	for k := range regKeys {
		if _, ok := srcKeys[k]; !ok {
			t.Fatalf("missing source for %s", k)
		}
	}
}

func TestWalkAllBundledWorkflows(t *testing.T) {
	root := configlayout.FindModuleRoot()
	reg, sources, err := workflowvalidate.CatalogSources(workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModeBundled,
	})
	testutil.FailErr(t, "CatalogSources", err)
	_ = reg
	keys := make([]string, 0, len(sources))
	for _, s := range sources {
		keys = append(keys, s.Key)
	}
	sort.Strings(keys)
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModeBundled,
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	byKey := map[string][]api.ComposeValidationError{}
	for _, d := range diags {
		// Field is "path: …" or "key: …" — attribute to first matching source path/key.
		attributed := false
		for _, s := range sources {
			if len(d.Field) >= len(s.Path) && (d.Field == s.Path || len(s.Path) > 0 && containsPrefix(d.Field, s.Path)) {
				byKey[s.Key] = append(byKey[s.Key], d)
				attributed = true
				break
			}
			if containsPrefix(d.Field, s.Key) {
				byKey[s.Key] = append(byKey[s.Key], d)
				attributed = true
				break
			}
		}
		if !attributed {
			byKey["*"] = append(byKey["*"], d)
		}
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if ds := byKey[key]; len(ds) > 0 {
				for _, d := range ds {
					t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
				}
				t.Fatalf("%d diagnostics", len(ds))
			}
		})
	}
	if ds := byKey["*"]; len(ds) > 0 {
		t.Run("catalog", func(t *testing.T) {
			for _, d := range ds {
				t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
			}
			t.Fatalf("%d catalog-level diagnostics", len(ds))
		})
	}
}

func containsPrefix(s, prefix string) bool {
	return len(prefix) > 0 && len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
