package hostresources

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContainerDaemonsAreSupportedWhereConfinementReachesSockets(t *testing.T) {
	for _, tc := range []struct {
		platform string
		want     HostSupport
	}{
		{"macos", HostSupported},
		{"linux", HostSupported},
		{"windows", HostUnsupported},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			snapshot := stockSnapshotWithDaemons(t, tc.platform)
			for _, id := range []string{"docker", "podman"} {
				state, ok := snapshotState(snapshot, id)
				if !ok {
					t.Fatalf("%s missing from the %s snapshot", id, tc.platform)
				}
				if id == "podman" && tc.platform == "windows" {
					// The Windows podman realization is a dynamic client, not a socket route.
					continue
				}
				if state.Status != StatusAvailable || state.HostSupport != tc.want {
					t.Fatalf("%s on %s: status=%s support=%s, want available/%s", id, tc.platform, state.Status, state.HostSupport, tc.want)
				}
			}
		})
	}
}

func TestDirectIPRoutesAreSupportedWhereConfinementReachesSockets(t *testing.T) {
	route := Connection{Mode: ConnectionDirectIP}
	for platform, want := range map[string]bool{"macos": true, "linux": true, "windows": false} {
		if got := defaultConnectionSupport(platform, route); got != want {
			t.Fatalf("direct_ip on %s = %v, want %v", platform, got, want)
		}
	}
}

// stockSnapshotWithDaemons discovers the bundled catalog as if every container
// client were installed and its daemon socket or pipe present.
func stockSnapshotWithDaemons(t *testing.T, platform string) Snapshot {
	t.Helper()
	home := t.TempDir()
	service, err := NewService(t.TempDir())
	testutil.FailErr(t, "new service", err)
	service.env.platform = platform
	service.env.homeDir = home
	service.env.runtimeDir = ""
	service.env.lookPath = func(name string) (string, error) {
		if name == "docker" || name == "podman" {
			return filepath.Join("/usr/local/bin", name), nil
		}
		return "", os.ErrNotExist
	}
	service.env.stat = func(path string) (os.FileInfo, error) {
		info, statErr := os.Stat(home)
		if statErr != nil {
			return nil, statErr
		}
		return fakeFileInfo{FileInfo: info, mode: os.ModeSocket}, nil
	}
	service.env.namedPipeAvailable = func(string) (bool, error) { return true, nil }
	return service.Snapshot(context.Background(), false)
}

func snapshotState(snapshot Snapshot, id string) (State, bool) {
	for _, state := range snapshot.Resources {
		if state.ID == id {
			return state, true
		}
	}
	return State{}, false
}
