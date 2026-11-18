package tools

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoadedToolsStateWhatLoadedAndHow(t *testing.T) {
	out := &ToolInvocationOut{}
	stateLoadedTools(ToolContext{Effects: InvocationEffects{Out: out}}, false, []string{"fetch_url", "web_search", "page_open", "mcp_github_create_issue"})
	found, loaded := out.CardFacts[0], out.CardFacts[1]
	if found.Kind != api.ActionFactFound || found.Count != 4 || found.Noun != "tools" || !reflect.DeepEqual(found.Items, []string{"Fetch page", "Search the web", "Open page"}) {
		t.Fatalf("found %+v", found)
	}
	if loaded.Kind != api.ActionFactLoadedBy || loaded.By != "engine" {
		t.Fatalf("loaded by %+v", loaded)
	}
	stateLoadedTools(ToolContext{Effects: InvocationEffects{Out: out}}, true, []string{"mcp_github_create_issue"})
	if len(out.CardFacts) != 2 || out.CardFacts[0].Items[0] != "mcp_github_create_issue" || out.CardFacts[1].By != "name" {
		t.Fatalf("a later statement did not replace the earlier one: %+v", out.CardFacts)
	}
}
