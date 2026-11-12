//go:build darwin || linux

package hostresources

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func clientEnvCatalog(connection string) string {
	return `version: 1
resources:
  - id: engine
    family: containers.local
    label: Engine
    category: Containers
    description: Engine.
    surfaces: [process_exec]
    realizations:
      - discover:
          path:
            kind: socket
            paths: ["/run/engine.sock"]
            capture: daemon_socket
        connections:
` + connection
}

// client_env names a variable for a local Unix socket's client and nothing else.
func TestCatalogClientEnvIsOnlyALocalSocketClientVariable(t *testing.T) {
	valid := "          - {mode: local_service, transport: unix_socket, target_from: daemon_socket, client_env: ENGINE_HOST}\n"
	if _, err := ParseCatalog([]byte(clientEnvCatalog(valid)), "test"); err != nil {
		t.Fatalf("valid client_env refused: %v", err)
	}
	for name, connection := range map[string]string{
		"proxy":    "          - {mode: proxy, client_env: ENGINE_HOST}\n",
		"reserved": "          - {mode: local_service, transport: unix_socket, target_from: daemon_socket, client_env: PATH}\n",
		"invalid":  "          - {mode: local_service, transport: unix_socket, target_from: daemon_socket, client_env: \"A-B\"}\n",
	} {
		if _, err := ParseCatalog([]byte(clientEnvCatalog(connection)), "test"); err == nil {
			t.Errorf("%s client_env accepted", name)
		}
	}
}

// A declared resource points its client at the socket the action reviews.
func TestResolutionPointsTheClientAtTheReviewedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pw-engine-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", socket)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = listener.Close() })
	def := Definition{
		ID: "engine", Family: "containers.local", Label: "Engine", Category: "Containers", Description: "Engine.",
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
		Realizations: []Realization{{
			Discover:    Expression{Path: &PathProbe{Kind: "socket", Paths: []string{socket}, Capture: "daemon_socket"}},
			Connections: []ConnectionTemplate{{Mode: ConnectionLocalService, Transport: LocalServiceUnixSocket, TargetFrom: "daemon_socket", ClientEnv: "ENGINE_HOST"}},
		}},
		origin: "test",
	}
	service := serviceForPath(t, def, nil)
	resolution, unmet := service.ResolveAction(context.Background(), []string{"engine"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec})
	if len(unmet) != 0 {
		t.Fatalf("unmet = %v", unmet)
	}
	_, reference := confine.SocketEndpointReferences(socket)
	if want := []string{"ENGINE_HOST=" + reference}; !slices.Equal(resolution.ClientEnv, want) {
		t.Fatalf("ClientEnv = %v, want %v", resolution.ClientEnv, want)
	}
	if len(resolution.Connections.LocalServices) != 1 || !strings.HasSuffix(reference, resolution.Connections.LocalServices[0].Target+"}}") {
		t.Fatalf("client variable and reviewed socket differ: %v %+v", resolution.ClientEnv, resolution.Connections)
	}
}

// A refused socket maps to the available resource whose service it is, by
// any spelling that resolves to the same socket.
func TestResourcesForSocketsNamesTheOwningResource(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pw-engine-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", socket)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = listener.Close() })
	link := filepath.Join(dir, "linked.sock")
	testutil.FailErr(t, "link socket", os.Symlink(socket, link))
	def := Definition{
		ID: "engine", Family: "containers.local", Label: "Engine", Category: "Containers", Description: "Engine.",
		Surfaces: []ExecutionSurface{SurfaceProcessExec},
		Realizations: []Realization{{
			Discover:    Expression{Path: &PathProbe{Kind: "socket", Paths: []string{socket}, Capture: "daemon_socket"}},
			Connections: []ConnectionTemplate{{Mode: ConnectionLocalService, Transport: LocalServiceUnixSocket, TargetFrom: "daemon_socket", ClientEnv: "ENGINE_HOST"}},
		}},
		origin: "test",
	}
	service := serviceForPath(t, def, nil)
	for _, path := range []string{socket, link} {
		if got := service.ResourcesForSockets(context.Background(), ProjectContext{}, []string{"/run/other.sock", path}); !slices.Equal(got, []string{"engine"}) {
			t.Errorf("%s: resources = %v", path, got)
		}
	}
	if got := service.ResourcesForSockets(context.Background(), ProjectContext{}, []string{"/run/other.sock"}); len(got) != 0 {
		t.Errorf("unrelated socket named %v", got)
	}
	// Without a client variable, declaring the resource would not point a
	// client at the socket, so the refusal must not suggest it.
	def.Realizations[0].Connections[0].ClientEnv = ""
	service = serviceForPath(t, def, nil)
	if got := service.ResourcesForSockets(context.Background(), ProjectContext{}, []string{socket}); len(got) != 0 {
		t.Errorf("resource without a client variable named %v", got)
	}
}
