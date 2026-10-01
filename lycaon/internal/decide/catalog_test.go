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
	policies, err := decide.LoadPolicies()
	testutil.FailErr(t, "load policies", err)
	for _, site := range decide.Sites() {
		p, ok := policies[site]
		if !ok {
			t.Fatalf("site %s missing from the shipped catalog", site)
		}
		if p.Deadline <= 0 || p.MaxCandidates <= 0 || p.Chunk <= 0 {
			t.Fatalf("site %s carries an unusable policy: %+v", site, p)
		}
	}
}

func TestParsePoliciesRejectsUnknownAndMissingSites(t *testing.T) {
	t.Parallel()
	_, err := decide.ParsePolicies([]byte("version: 2\nrerank:\n  nowhere:\n    enabled: true\n    deadline_ms: 1\n    max_candidates: 1\n    chunk: 1\n    weight: 1\n"))
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

func TestParsePoliciesValidatesBudgets(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("version: 2\nrerank:\n")
	for _, site := range decide.Sites() {
		b.WriteString("  " + string(site) + ":\n    enabled: true\n    deadline_ms: 100\n    max_candidates: 8\n    chunk: 4\n    weight: 0.5\n")
	}
	good := b.String()
	policies, err := decide.ParsePolicies([]byte(good))
	testutil.FailErr(t, "parse valid catalog", err)
	if p := policies.For(decide.SiteRepomapTags); p.Deadline != 100*time.Millisecond || p.MaxCandidates != 8 || p.Chunk != 4 || p.Weight != 0.5 || !p.Enabled {
		t.Fatalf("policy = %+v", p)
	}
	bad := strings.Replace(good, "chunk: 4", "chunk: 9", 1)
	if _, err := decide.ParsePolicies([]byte(bad)); err == nil || !strings.Contains(err.Error(), "chunk must be in (0, max_candidates]") {
		t.Fatalf("oversized chunk accepted: %v", err)
	}
	if _, err := decide.ParsePolicies([]byte(strings.Replace(good, "version: 2", "version: 1", 1))); err == nil {
		t.Fatal("wrong version accepted")
	}
	if _, err := decide.ParsePolicies([]byte(good + "extra: 1\n")); err == nil {
		t.Fatal("unknown top-level key accepted")
	}
}

func TestPoliciesForUndeclaredSiteIsDisabled(t *testing.T) {
	t.Parallel()
	var none decide.Policies
	if none.For(decide.SiteWebPages).Enabled {
		t.Fatal("nil policies must disable every site")
	}
}
