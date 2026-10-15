package decide_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestShippedCatalogDeclaresEverySite(t *testing.T) {
	t.Parallel()
	catalog, err := decide.LoadCatalog()
	testutil.FailErr(t, "load catalog", err)
	for _, site := range decide.Sites() {
		p, ok := catalog.Policies[site]
		if !ok {
			t.Fatalf("site %s missing from the shipped catalog", site)
		}
		if p.Deadline <= 0 || p.MaxCandidates <= 0 || p.Chunk <= 0 {
			t.Fatalf("site %s carries an unusable policy: %+v", site, p)
		}
	}
	if catalog.Engine.Reference <= 0 {
		t.Fatalf("engine requirement = %+v", catalog.Engine)
	}
}

func TestParseCatalogRejectsUnknownAndMissingSites(t *testing.T) {
	t.Parallel()
	_, err := decide.ParseCatalog([]byte("version: 2\nengine:\n  reference_ms: 1\nrerank:\n  nowhere:\n    enabled: true\n    deadline_ms: 1\n    max_candidates: 1\n    chunk: 1\n    weight: 1\n"))
	if err == nil {
		t.Fatal("unknown site must fail")
	}
	if !strings.Contains(err.Error(), "rerank.nowhere is not a reranking site") {
		t.Fatalf("unknown site fault missing: %v", err)
	}
	if !strings.Contains(err.Error(), "rerank.summarize_definitions is not declared") {
		t.Fatalf("missing site fault missing: %v", err)
	}
}

func catalogWith(referenceMS, deadlineMS string) string {
	var b strings.Builder
	b.WriteString("version: 2\nengine:\n  reference_ms: " + referenceMS + "\nrerank:\n")
	for _, site := range decide.Sites() {
		b.WriteString("  " + string(site) + ":\n    enabled: true\n    deadline_ms: " + deadlineMS + "\n    max_candidates: 8\n    chunk: 4\n    weight: 0.5\n")
	}
	return b.String()
}

func TestParseCatalogValidatesBudgets(t *testing.T) {
	t.Parallel()
	good := catalogWith("50", "100")
	catalog, err := decide.ParseCatalog([]byte(good))
	testutil.FailErr(t, "parse valid catalog", err)
	if p := catalog.Policies.For(decide.SiteRepomapTags); p.Deadline != 100*time.Millisecond || p.MaxCandidates != 8 || p.Chunk != 4 || p.Weight != 0.5 || !p.Enabled {
		t.Fatalf("policy = %+v", p)
	}
	if catalog.Engine.Reference != 50*time.Millisecond {
		t.Fatalf("engine = %+v", catalog.Engine)
	}
	bad := strings.Replace(good, "chunk: 4", "chunk: 9", 1)
	if _, err := decide.ParseCatalog([]byte(bad)); err == nil || !strings.Contains(err.Error(), "chunk must be in (0, max_candidates]") {
		t.Fatalf("oversized chunk accepted: %v", err)
	}
	if _, err := decide.ParseCatalog([]byte(strings.Replace(good, "version: 2", "version: 1", 1))); err == nil {
		t.Fatal("wrong version accepted")
	}
	if _, err := decide.ParseCatalog([]byte(good + "extra: 1\n")); err == nil {
		t.Fatal("unknown top-level key accepted")
	}
	if _, err := decide.ParseCatalog([]byte(catalogWith("0", "100"))); err == nil || !strings.Contains(err.Error(), "engine.reference_ms must be positive") {
		t.Fatalf("missing requirement accepted: %v", err)
	}
}

func TestParseCatalogRefusesARequirementSlowerThanAnEnabledSite(t *testing.T) {
	t.Parallel()
	_, err := decide.ParseCatalog([]byte(catalogWith("150", "100")))
	if err == nil || !strings.Contains(err.Error(), "deadline_ms 100 cannot fit 1 reference batches of engine.reference_ms 150") {
		t.Fatalf("a requirement no site can meet was accepted: %v", err)
	}
	disabled := strings.ReplaceAll(catalogWith("150", "100"), "enabled: true", "enabled: false")
	_, err = decide.ParseCatalog([]byte(disabled))
	testutil.FailErr(t, "disabled sites carry no deadline obligation", err)
}

func TestPoliciesForUndeclaredSiteIsDisabled(t *testing.T) {
	t.Parallel()
	var none decide.Policies
	if none.For(decide.SiteWebPages).Enabled {
		t.Fatal("nil policies must disable every site")
	}
}
