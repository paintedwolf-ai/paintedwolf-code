package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubCheckoutUsageRecorder struct {
	relations []CheckoutUsageRelation
	notified  []string
	resolved  map[string]ResolvedCheckoutPath
}

func (s *stubCheckoutUsageRecorder) RecordCheckoutActivity(_ context.Context, _ ToolContext, _ string, _ string, relation CheckoutUsageRelation) string {
	s.relations = append(s.relations, relation)
	return string(relation) + "-wt"
}

func (s *stubCheckoutUsageRecorder) NotifyCheckoutChange(_ context.Context, _ ToolContext, repoPath string) {
	s.notified = append(s.notified, repoPath)
}

func (s *stubCheckoutUsageRecorder) ResolvePathCheckout(_ context.Context, _ string, targetPath string) (ResolvedCheckoutPath, bool) {
	out, ok := s.resolved[targetPath]
	return out, ok
}

func TestCheckoutUsageForwarding(t *testing.T) {
	var empty ToolContext
	if empty.RecordCheckoutCreated(t.Context(), "/tmp/wt", "act-1") != "" || empty.RecordCheckoutUsed(t.Context(), "/tmp/wt", "act-1") != "" {
		t.Fatal("nil recorder returned checkout id")
	}
	empty.NotifyCheckoutChange(t.Context(), "/tmp/repo")

	wtDir := t.TempDir()
	absFile := filepath.Join(wtDir, "src", "app.ts")
	rec := &stubCheckoutUsageRecorder{
		resolved: map[string]ResolvedCheckoutPath{
			absFile: {RepoID: "repo-1", WorktreeID: "11111111-1111-4111-8111-111111111111", RootID: "root-main", RootRel: "src/app.ts"},
		},
	}
	tc := ToolContext{
		Identity: InvocationIdentity{ProjectID: "p"},
		Source: InvocationSource{
			CheckoutUsage: rec,
			Roots: []projectroot.RootRef{
				{ID: "root-wt", Path: wtDir},
			},
		},
		Effects: InvocationEffects{Out: &ToolInvocationOut{}},
	}
	if got := tc.RecordCheckoutCreated(t.Context(), wtDir, "act-1"); got != "created-wt" {
		t.Fatalf("created id = %q", got)
	}
	if got := tc.RecordCheckoutUsed(t.Context(), wtDir, "act-2"); got != "used-wt" {
		t.Fatalf("used id = %q", got)
	}
	tc.NotifyCheckoutChange(t.Context(), wtDir)
	if len(rec.relations) != 2 || rec.relations[0] != CheckoutUsageCreated || rec.relations[1] != CheckoutUsageUsed || len(rec.notified) != 1 {
		t.Fatalf("recorder state = %+v", rec)
	}

	tc.RecordSourceIdentity("root-wt", "src/app.ts", api.NavigationEntryKindFile)
	tc.RecordSourcePath(absFile, api.NavigationEntryKindFile)
	tc.RecordSourceIdentity("root-wt", "unmapped.ts", api.NavigationEntryKindFile)
	locs := tc.Effects.Out.SourceContext.Locations
	if len(locs) != 2 || locs[0].WorktreeID != "11111111-1111-4111-8111-111111111111" || locs[0].RootID != "root-main" {
		t.Fatalf("enriched locations = %+v", locs)
	}
}

func TestSourceContextDeduplicatesBeforeBounding(t *testing.T) {
	c := ToolContext{
		Identity: InvocationIdentity{ProjectID: "p"},
		Effects:  InvocationEffects{Out: &ToolInvocationOut{}},
	}
	target := api.NavigationTarget{ProjectID: "p", RootID: "r", Path: ".hidden/file", EntryKind: api.NavigationEntryKindFile}
	for i := 0; i < api.MaxSourceContextLocations+10; i++ {
		c.RecordSourceLocation(target)
	}
	if got := c.Effects.Out.SourceContext; got.Truncated || len(got.Locations) != 1 {
		t.Fatalf("duplicate locations exhausted context: %+v", got)
	}
	c.RecordSourceContext(&api.SourceContext{Locations: []api.NavigationTarget{target}, Truncated: true})
	if !c.Effects.Out.SourceContext.Truncated {
		t.Fatal("recall lost incomplete source context")
	}
}
