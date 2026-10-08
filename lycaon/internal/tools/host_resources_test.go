package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/oar"
)

func TestExpandHostResourceConnections(t *testing.T) {
	executor := NewDefaultToolExecutor(nil, nil, "")
	executor.SetHostResourceConnectionSource(func(
		context.Context,
		[]string,
		hostresources.ProjectContext,
		[]hostresources.ExecutionSurface,
	) (hostresources.ActionResolution, []string) {
		return hostresources.ActionResolution{Connections: hostresources.ConnectionRequest{
			LocalServices: []hostresources.LocalServiceEndpoint{{
				Transport: hostresources.LocalServiceUnixSocket,
				Target:    "/tmp/docker.sock",
			}},
			DirectDestinations: []string{"private.example:443"},
		}}, nil
	})
	args := map[string]any{
		"command": "docker info",
		"capability_request": map[string]any{
			"host_resources": []any{"docker"},
			"socket_paths":   []any{"/tmp/other.sock"},
		},
	}
	got, _, err := executor.expandHostResourceConnections(context.Background(), "command", args, ToolContext{})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	request := got["capability_request"].(map[string]any)
	if !reflect.DeepEqual(request["socket_paths"], []string{"/tmp/docker.sock", "/tmp/other.sock"}) {
		t.Fatalf("socket_paths = %#v", request["socket_paths"])
	}
	direct := request["direct_ip"].(map[string]any)
	if !reflect.DeepEqual(direct["declared_destinations"], []string{"private.example:443"}) {
		t.Fatalf("direct_ip = %#v", direct)
	}
	if _, present := args["capability_request"].(map[string]any)["direct_ip"]; present {
		t.Fatal("input args mutated")
	}
}

func TestExpandHostResourceConnectionsDeniesBeforeRouteExpansion(t *testing.T) {
	executor := NewDefaultToolExecutor(nil, nil, "")
	executor.SetHostResourceConnectionSource(func(
		context.Context,
		[]string,
		hostresources.ProjectContext,
		[]hostresources.ExecutionSurface,
	) (hostresources.ActionResolution, []string) {
		return hostresources.ActionResolution{
			States: map[string]hostresources.State{
				"docker": {ID: "docker", Status: hostresources.StatusAvailable, Access: hostresources.AccessDeny},
			},
			Connections: hostresources.ConnectionRequest{LocalServices: []hostresources.LocalServiceEndpoint{{
				Transport: hostresources.LocalServiceUnixSocket,
				Target:    "/tmp/docker.sock",
			}}},
			Deny: []string{"docker"},
		}, nil
	})
	args := map[string]any{
		"command":            "docker info",
		"capability_request": map[string]any{"host_resources": []any{"docker"}},
	}

	got, resolution, err := executor.expandHostResourceConnections(context.Background(), "command", args, ToolContext{})
	var reject *ToolReject
	if !errors.As(err, &reject) || reject.Code != "HOST_RESOURCE_POLICY_DENIED" {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(reject.Data["action_host_resource_denials"], resolution.Deny) {
		t.Fatalf("fallback rejection lost denied resource facts: %v", reject.Data)
	}
	if got != nil || resolution == nil || len(resolution.Deny) != 1 {
		t.Fatalf("denied request was expanded: got=%v resolution=%+v", got, resolution)
	}
	if _, present := args["capability_request"].(map[string]any)["socket_paths"]; present {
		t.Fatal("denied host resource widened the route request")
	}
}

func TestObserveHostResourceResolutionPublishesExternalAuthorizationOutcome(t *testing.T) {
	t.Parallel()

	resolution := hostresources.ActionResolution{
		States: map[string]hostresources.State{
			"podman": {ID: "podman", Status: hostresources.StatusUnknown, Access: hostresources.AccessAsk},
			"docker": {ID: "docker", Status: hostresources.StatusAvailable, Access: hostresources.AccessDeny},
		},
		Deny: []string{"docker"},
	}
	gc := &oar.GuardContext{}
	observeHostResourceResolution(gc, resolution)

	if !reflect.DeepEqual(gc.Access.ActionHostResources, []string{"docker", "podman"}) {
		t.Fatalf("action host resources = %#v", gc.Access.ActionHostResources)
	}
	if !reflect.DeepEqual(gc.Access.ActionHostResourceDenials, []string{"docker"}) {
		t.Fatalf("action host-resource denials = %#v", gc.Access.ActionHostResourceDenials)
	}
	if got := gc.Access.HostResourceStatus["podman"]; got != "unknown" {
		t.Fatalf("podman status = %q, want unknown", got)
	}
	if got := gc.Access.HostResourcePolicy["docker"]; got != "deny" {
		t.Fatalf("docker policy = %q, want deny", got)
	}
}
