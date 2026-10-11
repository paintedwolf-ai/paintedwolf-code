package tools

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

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
