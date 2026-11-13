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
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "host-resources.yaml"))
	testutil.FailErr(t, "read shipped catalog", err)
	catalog, err := ParseCatalog(data, "built-in")
	testutil.FailErr(t, "parse shipped catalog", err)
	for _, resource := range catalog.Resources {
		for _, realization := range resource.Realizations {
			for _, connection := range realization.Connections {
				if connection.Mode == ConnectionLocalService && connection.Transport == LocalServiceUnixSocket && connection.ClientEnv == "" {
					t.Errorf("%s: a local socket without a client variable leaves its client on its own endpoint", resource.ID)
				}
			}
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
