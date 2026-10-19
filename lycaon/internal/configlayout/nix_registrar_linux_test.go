package configlayout

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestNixRegistrarSelectsOnlyTheInstalledPhysicalAuthority(t *testing.T) {
	directory := func(uid uint32, mode uint32) nixStorageFact {
		return nixStorageFact{kind: unix.S_IFDIR, uid: uid, mode: mode}
	}
	owned := directory(1000, 0o755)
	root := directory(0, 0o755)
	socket := nixStorageFact{kind: unix.S_IFSOCK, uid: 0, mode: 0o666}
	for _, tc := range []struct {
		name                         string
		store, state, parent, socket nixStorageFact
		prefix                       string
	}{
		{"single user", owned, owned, nixStorageFact{}, nixStorageFact{}, "local?"},
		{"system daemon", directory(0, 0o1775), root, root, socket, "unix://" + nixDaemonSocket + "?"},
		{"missing daemon", root, root, root, nixStorageFact{}, ""},
		{"foreign daemon", root, root, root, nixStorageFact{kind: unix.S_IFSOCK, uid: 2000}, ""},
		{"writable daemon parent", root, root, directory(0, 0o777), socket, ""},
		{"foreign store", directory(2000, 0o755), root, root, socket, ""},
		{"unsafe state", root, directory(0, 0o777), root, socket, ""},
		{"nonstandard sticky store", directory(0, 0o1777), root, root, socket, ""},
		{"missing state", root, nixStorageFact{}, root, socket, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registrar, err := selectNixRegistrar(1000, tc.store, tc.state, tc.parent, tc.socket)
			if tc.prefix == "" {
				if err == nil {
					t.Fatal("unsupported authority accepted")
				}
				return
			}
			testutil.FailErr(t, "select registrar", err)
			if !strings.HasPrefix(registrar.storeURI, tc.prefix) {
				t.Fatalf("wrong physical authority: %q", registrar.storeURI)
			}
			settings, err := url.ParseQuery(strings.SplitN(registrar.storeURI, "?", 2)[1])
			testutil.FailErr(t, "read explicit settings", err)
			if settings.Get("store") != nixPhysicalStore || settings.Get("real") != nixPhysicalStore || settings.Get("state") != nixPhysicalState {
				t.Fatal("registration depends on implicit storage settings")
			}
		})
	}
	if _, err := runningNixRegistrar(&NixLayout{Output: "/custom/store/output"}); err == nil {
		t.Fatal("custom physical authority inferred without an installation fact")
	}
}

func poisonNixCallerConfiguration(t *testing.T) {
	t.Helper()
	config := t.TempDir()
	testutil.FailErr(t, "write caller config", os.WriteFile(filepath.Join(config, "nix.conf"),
		[]byte("store = dummy://\nplugin-files = /missing-caller-plugin.so\n"), 0o600))
	for _, name := range []string{"HOME", "NIX_CONF_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(name, config)
	}
	for _, name := range []string{"NIX_REMOTE", "NIX_STORE_DIR", "NIX_STATE_DIR", "NIX_DAEMON_SOCKET_PATH", "NIX_USER_CONF_FILES", "NIX_PLUGIN_FILES"} {
		t.Setenv(name, "/missing-caller-override")
	}
	t.Setenv("NIX_CONFIG", "store = dummy://\nplugin-files = /missing-caller-plugin.so\n")
	t.Setenv("LYCAON_API_TOKEN", "private-engine-token")
	t.Setenv("SSH_AUTH_SOCK", "/private-caller-agent")
}

func TestNixRegistrarClosesCallerConfigurationBeforeExecution(t *testing.T) {
	poisonNixCallerConfiguration(t)
	root := t.TempDir()
	tool := filepath.Join(root, "nix-store")
	program := `#!/bin/sh
[ "$1" = --store ] && [ "$2" = 'local?store=/nix/store' ] || exit 1
[ -z "${LYCAON_API_TOKEN+x}${NIX_REMOTE+x}${NIX_STORE_DIR+x}${NIX_STATE_DIR+x}${NIX_DAEMON_SOCKET_PATH+x}${SSH_AUTH_SOCK+x}${NIX_PLUGIN_FILES+x}" ] || exit 2
[ "$HOME" = "$NIX_CONF_DIR" ] && [ "$HOME" = "$XDG_CONFIG_HOME" ] || exit 3
[ -d "$NIX_CONF_DIR" ] && [ ! -e "$NIX_CONF_DIR/nix.conf" ] || exit 4
[ "$NIX_USER_CONF_FILES" = /dev/null ] || exit 5
[ "$NIX_CONFIG" = 'substitute = false
substituters =
builders =
max-jobs = 0
plugin-files =
' ] || exit 6
ln -s "$7" "$4"
`
	testutil.FailErr(t, "write fixture registrar", os.WriteFile(tool, []byte(program), 0o700))
	retention, err := retainNix(context.Background(), &NixLayout{Output: "/selected-generation", NixStore: tool},
		filepath.Join(root, "roots"), nixRegistrar{storeURI: "local?store=/nix/store"})
	testutil.FailErr(t, "register with closed configuration", err)
	retention.Close()
}

func TestNixRegistrarNativePhysicalRegistrationIgnoresCallerOverrides(t *testing.T) {
	tool := os.Getenv("PW_NIX_RETENTION_TEST_TOOL")
	if tool == "" {
		t.Skip("requires an explicitly selected native Nix client")
	}
	tool, err := filepath.EvalSymlinks(tool)
	testutil.FailErr(t, "resolve selected native client", err)
	layout := &NixLayout{Output: filepath.Dir(filepath.Dir(tool)), NixStore: tool}
	registrar, err := runningNixRegistrar(layout)
	testutil.FailErr(t, "select actual physical GC authority", err)
	poisonNixCallerConfiguration(t)
	retention, err := retainNix(context.Background(), layout, filepath.Join(t.TempDir(), "roots"), registrar)
	testutil.FailErr(t, "register using actual Nix with caller config disabled", err)
	defer retention.Close()
	link := strings.TrimSuffix(retention.LockPath, ".lock")
	entries, err := os.ReadDir(filepath.Join(nixPhysicalState, "gcroots", "auto"))
	testutil.FailErr(t, "read physical root registry", err)
	found := false
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(nixPhysicalState, "gcroots", "auto", entry.Name()))
		if err == nil && target == link {
			found = true
		}
	}
	if !found {
		t.Fatal("Nix did not register the root with the actual physical GC authority")
	}
}
