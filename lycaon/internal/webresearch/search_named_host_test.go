package webresearch

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// dialingDiscoverer stands in for the built-in search reaching hosts.
type dialingDiscoverer struct{ hosts []string }

func (d dialingDiscoverer) Search(ctx context.Context, _ DirectRequest) ([]WebHit, error) {
	for _, host := range d.hosts {
		if err := egressgate.AwaitHost(ctx, host); err != nil {
			return nil, err
		}
	}
	return []WebHit{{Title: "Doc", URL: "https://result.example/doc", Snippet: "guide", Provider: "direct"}}, nil
}

// A host the query's site operator names is the agent's own address, as is a
// host beneath it. Every other host a search reaches is one of its sources.
func TestSearchSiteOperatorHostIsTheAgentsOwnAddress(t *testing.T) {
	gate := recordGate(t, true)
	reached := []string{"named.example", "www.named.example", "result.example", "unnamed.example"}
	reg := tools.NewDefaultRegistry()
	factory := func(context.Context, tools.ToolContext) DirectDiscoverer { return dialingDiscoverer{hosts: reached} }
	testutil.FailErr(t, "RegisterToolsWithFactory",
		RegisterToolsWithFactory(reg, ToolDeps{}, func() DirectDiscovererFactory { return factory }))

	_, err := reg.Run(context.Background(), "web_search", map[string]any{"query": "guide site:Named.example"},
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call"}})
	testutil.FailErr(t, "web_search", err)

	dials := gate.reached()
	var gated []string
	for i, dial := range dials {
		host := gate.hosts[i]
		gated = append(gated, host)
		if dial.OriginOf(host) != confine.AddressAuthored || confine.RetrievalDial(dial, host) {
			t.Errorf("%s reached the gate as a source: %+v", host, dial)
		}
	}
	if want := []string{"named.example", "www.named.example"}; !slices.Equal(gated, want) {
		t.Fatalf("hosts at the gate = %v, want %v", gated, want)
	}
}

func TestQueryNamedHosts(t *testing.T) {
	for query, want := range map[string][]string{
		"plain words":                                  nil,
		"guide site:docs.example.org":                  {"docs.example.org"},
		"site:https://Docs.Example.org/deep/page x":    {"docs.example.org"},
		"site:a.example site:b.example site:a.example": {"a.example", "b.example"},
		"site:localhost":                               nil,
	} {
		if got := queryNamedHosts(query); !slices.Equal(got, want) {
			t.Errorf("queryNamedHosts(%q) = %v, want %v", query, got, want)
		}
	}
}
