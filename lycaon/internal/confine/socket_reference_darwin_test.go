//go:build darwin

package confine_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/socketref"
)

func TestSocketReferenceAliasCannotBeMutatedFromBroadWriteRoot(t *testing.T) {
	self := requireSeatbelt(t)
	for _, mutation := range []string{"unlink-alias", "rename-alias", "rename-private-directory", "rename-alias-root", "rename-config-root", "rename-ancestor"} {
		t.Run(mutation, func(t *testing.T) {
			broad := shortTempDir(t)
			ancestor := filepath.Join(broad, "ancestor")
			state := filepath.Join(ancestor, "state")
			t.Setenv("LYCAON_CONFIG_DIR", state)
			path := filepath.Join(broad, "service.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			lease, err := socketref.Capture(path)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			aliases, err := filepath.Glob(filepath.Join(state, "socket-references", "service-*", "socket"))
			if err != nil || len(aliases) != 1 {
				t.Fatalf("alias fixture: %v, %v", aliases, err)
			}
			alias := aliases[0]
			source := alias
			switch mutation {
			case "rename-private-directory":
				source = filepath.Dir(alias)
			case "rename-alias-root":
				source = filepath.Dir(filepath.Dir(alias))
			case "rename-config-root":
				source = state
			case "rename-ancestor":
				source = ancestor
			}
			unsafe := confine.Confinement{Roots: []string{broad}, Network: confine.NetworkDeny}
			cmd, cleanup, err := confine.Command(context.Background(), self, "/bin/rm", []string{alias}, unsafe)
			cleanup()
			var refused *confine.WriteRootRefusalError
			if cmd != nil || !errors.As(err, &refused) || refused.Code != confine.WriteRootCodeContainsState {
				t.Fatalf("broad root entered control plane: cmd=%v, error=%v", cmd, err)
			}
			project := filepath.Join(broad, "project")
			if err := os.Mkdir(project, 0700); err != nil {
				t.Fatal(err)
			}
			c := confine.Confinement{Roots: []string{project}, Network: confine.NetworkDeny}
			var code int
			var output string
			if mutation == "unlink-alias" {
				code, output = confinedRun(t, self, c, "/bin/rm", alias)
			} else {
				destination := filepath.Join(broad, "relocated")
				code, output = confinedRun(t, self, c, "/bin/mv", source, destination)
				if code == 0 {
					if err := os.Rename(destination, source); err != nil {
						t.Fatalf("restore owned fixture: %v", err)
					}
				}
			}
			if code == 0 {
				t.Fatalf("confined alias mutation succeeded: %s", output)
			}
			client, err := lease.DialContext(t.Context())
			if err != nil {
				t.Fatalf("retained original after refused mutation: %v", err)
			}
			defer client.Close()
			accepted, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			_ = accepted.Close()
		})
	}
}
