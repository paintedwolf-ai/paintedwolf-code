package hostos

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Each platform's declared set is pinned here; a platform not listed declares
// nothing.
func TestPlatformDeclaresItsCapabilities(t *testing.T) {
	full := Capabilities{System: true, Identity: true, NumericOwnership: true}
	want := map[string]Capabilities{"darwin": full, "linux": full, "windows": {}}[runtime.GOOS]
	if got := (Host{}).Capabilities(); got != want {
		t.Fatalf("%s declares %+v, want %+v", runtime.GOOS, got, want)
	}
}

// Every fact answers from the platform when declared and is ErrUnsupported
// otherwise.
func TestFactsFollowTheDeclaration(t *testing.T) {
	host := Host{}
	declared := host.Capabilities()
	dir := t.TempDir()
	file := filepath.Join(dir, "owned")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	system, err := host.System(t.Context())
	if !declared.System {
		requireUnsupported(t, "system", err)
	} else {
		hostname, hostErr := os.Hostname()
		if err != nil || hostErr != nil || system.Nodename != hostname || system.Sysname == "" || system.Machine == "" {
			t.Fatalf("system %+v (%v), hostname %q (%v)", system, err, hostname, hostErr)
		}
	}

	identity, err := host.Identity(t.Context())
	if !declared.Identity {
		requireUnsupported(t, "identity", err)
	} else if err != nil || identity.UID != os.Getuid() || identity.GID != os.Getgid() || len(identity.Groups) == 0 {
		t.Fatalf("identity %+v (%v)", identity, err)
	}

	uid, gid, err := host.CurrentIDs()
	if !declared.NumericOwnership {
		requireUnsupported(t, "current ids", err)
		_, err = host.LookupUser("root")
		requireUnsupported(t, "user lookup", err)
		_, err = host.LookupGroup("root")
		requireUnsupported(t, "group lookup", err)
		_, _, err = host.FileOwner(file)
		requireUnsupported(t, "file owner", err)
		return
	}
	if err != nil || uid != os.Getuid() || gid != os.Getgid() {
		t.Fatalf("current ids %d:%d (%v)", uid, gid, err)
	}
	owner, group, err := host.FileOwner(file)
	if err != nil || owner != uid || group < 0 {
		t.Fatalf("file owner %d:%d (%v)", owner, group, err)
	}
	if _, _, err := host.FileOwner(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file owner: %v", err)
	}
	current, err := user.Current()
	testutil.FailErr(t, "user.Current failed", err)
	if id, err := host.LookupUser(current.Username); err != nil || id != uid {
		t.Fatalf("lookup %s = %d (%v)", current.Username, id, err)
	}
	if name := identity.GroupNames[gid]; name != "" {
		if id, err := host.LookupGroup(name); err != nil || id != gid {
			t.Fatalf("lookup group %s = %d (%v)", name, id, err)
		}
	}
	for _, lookup := range []func(string) (int, error){host.LookupUser, host.LookupGroup} {
		if _, err := lookup("no-such-account-for-hostos"); !errors.Is(err, ErrUnknownAccount) {
			t.Fatalf("unknown account: %v", err)
		}
	}
}

func TestCanceledContextReadsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Host{}).System(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("system: %v", err)
	}
	if _, err := (Host{}).Identity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("identity: %v", err)
	}
	if n, err := (Host{}).Processors(ctx, true); err != nil || n < 1 {
		t.Fatalf("processors %d (%v)", n, err)
	}
}

func requireUnsupported(t *testing.T, fact string, err error) {
	t.Helper()
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%s on an undeclared platform: %v", fact, err)
	}
}
