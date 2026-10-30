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
	"github.com/lycaon/lycaon/internal/testutil"
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
			testutil.FailErr(t, "listen on fixture socket", err)
			defer listener.Close()
			lease, err := socketref.Capture(path)
			testutil.FailErr(t, "capture socket reference", err)
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
				testutil.FailErr(t, "create fixture directory", err)
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
			testutil.FailErr(t, "accept fixture socket connection", err)
			_ = accepted.Close()
		})
	}
}

func TestRetainedNativeGrantCannotBecomeSeatbeltPathnameAuthority(t *testing.T) {
	root := shortTempDir(t)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("native listener: %v", err)
	}
	defer listener.Close()
	lease, err := socketref.Capture(path)
	if err != nil {
		t.Fatalf("native reference: %v", err)
	}
	defer lease.Close()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("native resolved label: %v", err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	c := confine.Confinement{Roots: []string{workspace}, Network: confine.NetworkDeny, SocketGrants: []confine.SocketGrant{{ApprovedPath: path, ResolvedPath: resolved, Binding: lease.Binding()}}}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	command, cleanup, err := confine.Command(t.Context(), self, "/usr/bin/true", nil, c)
	defer cleanup()
	var refused *confine.SocketResolveError
	if command != nil || !errors.As(err, &refused) || refused.Code != confine.SocketResolveRefused {
		t.Fatalf("native grant became pathname command: command=%v, err=%v", command, err)
	}
}
