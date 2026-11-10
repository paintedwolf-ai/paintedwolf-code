//go:build darwin

package confine_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Only macOS references a socket through a private hard link. Linux retains a
// descriptor, which leaves no name to mutate.
func TestSocketReferenceAliasCannotBeMutatedFromBroadWriteRoot(t *testing.T) {
	self := requireLiveBoundary(t)
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
			for _, root := range []string{broad, filepath.Join(broad, "project")} {
				if root != broad {
					testutil.FailErr(t, "create fixture directory", os.MkdirAll(root, 0700))
				}
				c := confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}
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
					t.Fatalf("confined alias mutation succeeded under %s: %s", root, output)
				}
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

func TestShortSocketNamespaceIsProtectedWithoutAnySocketGrant(t *testing.T) {
	self := requireLiveBoundary(t)
	root := shortTempDir(t)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen for protected address fixture", err)
	defer listener.Close()
	owner, err := socketref.Capture(path)
	testutil.FailErr(t, "capture protected address fixture", err)
	defer owner.Close()
	endpoint, err := owner.Endpoint("unix")
	testutil.FailErr(t, "create protected short address", err)
	shortRoot := filepath.Dir(filepath.Dir(endpoint))
	workspace := filepath.Join(root, "workspace")
	testutil.FailErr(t, "create namespace probe workspace", os.Mkdir(workspace, 0700))
	future := shortRoot + "-future"
	c := confine.Confinement{Roots: []string{workspace}, GrantedWriteRoots: []string{shortRoot, future}, Network: confine.NetworkDeny}
	for _, target := range []string{endpoint, filepath.Join(shortRoot, "d"), shortRoot} {
		before, err := os.Lstat(target)
		testutil.FailErr(t, "inspect protected namespace before mutation", err)
		_, output := confinedRun(t, self, c, "/bin/rm", "-rf", target)
		after, err := os.Lstat(target)
		if err != nil || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
			t.Fatalf("unreviewed command changed service namespace %q: error=%v, output=%s", target, err, output)
		}
	}
	// Namespace protection exists before a particular reference is created.
	code, output := confinedRun(t, self, c, "/bin/mkdir", future)
	if _, err := os.Lstat(future); !os.IsNotExist(err) {
		_ = os.Remove(future)
		t.Fatalf("unreviewed command precreated service namespace: code=%d, error=%v, output=%s", code, err, output)
	}
	profile, err := confine.BuildSeatbeltProfile(c)
	testutil.FailErr(t, "render protected namespace floor", err)
	if !strings.Contains(profile, "paintedwolf-socket-") {
		t.Fatal("short namespace has no compiled control-plane floor")
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
	if command != nil || !errors.As(err, &refused) || refused.Code != confine.SocketResolveChanged {
		t.Fatalf("native grant became pathname command: command=%v, err=%v", command, err)
	}
}
