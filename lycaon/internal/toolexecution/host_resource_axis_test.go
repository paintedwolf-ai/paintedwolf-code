package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
)

// Host-resource resolution stamps ToolContext before the socket and direct-IP
// preflights read it; otherwise they resolve against an empty host-resource set.
func TestHostResourceResolutionStampsContextBeforeConnectionPreflight(t *testing.T) {
	executor := NewExecutor(nil, nil, "")
	executor.Network.SetHostResourceConnectionSource(func(
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
	merged, resolution, err := executor.Network.expandHostResourceConnections(
		context.Background(), "command", args, tools.ToolContext{},
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
	tc := tools.ToolContext{}
	resolution := hostresources.ActionResolution{
		States:   map[string]hostresources.State{"docker": {ID: "docker", Family: "containers.local"}},
		Families: []string{"containers.local"},
		Ask:      []string{"docker"},
	}
	tc.Host.HostResources = sortedHostResourceIDs(resolution.States)
	tc.Host.HostResourceFamilies = append([]string(nil), resolution.Families...)
	tc.Host.HostResourceAsk = append([]string(nil), resolution.Ask...)

	// A socket approval landing on the same action changes the network axes only.
	tc.Socket.SocketGrants = nil
	tc.Direct.DirectIPAuthorized = true

	if len(tc.Host.HostResourceAsk) != 1 || tc.Host.HostResourceAsk[0] != "docker" {
		t.Fatalf("HostResourceAsk = %v, want the resource ask untouched by a network approval", tc.Host.HostResourceAsk)
	}
}
