package tools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type stubCheckoutUsageRecorder struct {
	relations []CheckoutUsageRelation
	notified  []string
}

func (s *stubCheckoutUsageRecorder) RecordCheckoutActivity(_ context.Context, _ ToolContext, _ string, _ string, relation CheckoutUsageRelation) string {
	s.relations = append(s.relations, relation)
	return string(relation) + "-wt"
}

func (s *stubCheckoutUsageRecorder) NotifyCheckoutChange(_ context.Context, _ ToolContext, repoPath string) {
	s.notified = append(s.notified, repoPath)
}

func TestCheckoutUsageForwarding(t *testing.T) {
	var empty ToolContext
	if empty.RecordCheckoutCreated(t.Context(), "/tmp/wt", "act-1") != "" || empty.RecordCheckoutUsed(t.Context(), "/tmp/wt", "act-1") != "" {
		t.Fatal("nil recorder returned checkout id")
	}
	empty.NotifyCheckoutChange(t.Context(), "/tmp/repo")

	rec := &stubCheckoutUsageRecorder{}
	tc := ToolContext{Source: InvocationSource{CheckoutUsage: rec}}
	if got := tc.RecordCheckoutCreated(t.Context(), "/tmp/wt", "act-1"); got != "created-wt" {
		t.Fatalf("created id = %q", got)
	}
	if got := tc.RecordCheckoutUsed(t.Context(), "/tmp/wt", "act-2"); got != "used-wt" {
		t.Fatalf("used id = %q", got)
	}
	tc.NotifyCheckoutChange(t.Context(), "/tmp/repo")
	if len(rec.relations) != 2 || rec.relations[0] != CheckoutUsageCreated || rec.relations[1] != CheckoutUsageUsed || len(rec.notified) != 1 || rec.notified[0] != "/tmp/repo" {
		t.Fatalf("recorder state = %+v", rec)
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
