package tools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
)

// Host-resource resolution stamps ToolContext before the socket and direct-IP
// preflights read it; otherwise they resolve against an empty host-resource set.
func TestHostResourceResolutionStampsContextBeforeConnectionPreflight(t *testing.T) {
	executor := &DefaultToolExecutor{}
	executor.SetHostResourceConnectionSource(func(
		_ context.Context, ids []string, _ hostresources.ProjectContext, _ []hostresources.ExecutionSurface,
	) (hostresources.ActionResolution, []string) {
		states := map[string]hostresources.State{}
		for _, id := range ids {
			states[id] = hostresources.State{ID: id, Family: "containers.local"}
		}
		return hostresources.ActionResolution{
			States:    states,
			Families:  []string{"containers.local"},
			Ask:       []string{"docker"},
			PathExtra: []string{"/Applications/Vendor.app/Contents/Resources/bin"},
		}, nil
	})

	args := map[string]any{
		"capability_request": map[string]any{"host_resources": []any{"docker"}},
	}
	merged, resolution, err := executor.expandHostResourceConnections(
		context.Background(), "command", args, ToolContext{},
	)
	if err != nil {
		t.Fatalf("expandHostResourceConnections: %v", err)
	}
	if resolution == nil {
		t.Fatal("no resolution returned for a declared host resource")
	}
	if len(resolution.PathExtra) != 1 {
		t.Fatalf("PathExtra = %v, want the resolved install directory", resolution.PathExtra)
	}
	if _, ok := merged["capability_request"]; !ok {
		t.Fatal("merged args lost capability_request before connection preflight reads it")
	}
}

// Network approval does not satisfy an independent host-resource approval.
func TestHostResourceAskSurvivesNetworkApproval(t *testing.T) {
	tc := ToolContext{}
	resolution := hostresources.ActionResolution{
		States:   map[string]hostresources.State{"docker": {ID: "docker", Family: "containers.local"}},
		Families: []string{"containers.local"},
		Ask:      []string{"docker"},
	}
	tc.HostResources = sortedHostResourceIDs(resolution.States)
	tc.HostResourceFamilies = append([]string(nil), resolution.Families...)
	tc.HostResourceAsk = append([]string(nil), resolution.Ask...)

	// A socket approval landing on the same action changes the network axes only.
	tc.SocketGrants = nil
	tc.DirectIPAuthorized = true

	if len(tc.HostResourceAsk) != 1 || tc.HostResourceAsk[0] != "docker" {
		t.Fatalf("HostResourceAsk = %v, want the resource ask untouched by a network approval", tc.HostResourceAsk)
	}
}
