package confine_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveCredentialStoreDBusSocket(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	cfgDir := filepath.Join(home, ".config", "paintedwolf")
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "mkdir cfgDir", os.MkdirAll(cfgDir, 0o700))
	testutil.FailErr(t, "mkdir sshDir", os.MkdirAll(sshDir, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	shortDir, err := os.MkdirTemp("/tmp", "pw-dbus-") //nolint:usetesting // Socket path must fit sun_path.
	testutil.FailErr(t, "mkdir short socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
	shortDir = fspath.CanonicalPath(shortDir)

	busSock := filepath.Join(shortDir, "bus")
	ln, err := net.Listen("unix", busSock)
	testutil.FailErr(t, "listen bus", err)
	defer func() { _ = ln.Close() }()

	regularFile := filepath.Join(shortDir, "not-a-socket")
	testutil.FailErr(t, "write regular file", os.WriteFile(regularFile, []byte("x"), 0o600))

	cfgSock := filepath.Join(shortDir, "cfg.sock")
	cfgLn, err := net.Listen("unix", cfgSock)
	testutil.FailErr(t, "listen cfgSock", err)
	defer func() { _ = cfgLn.Close() }()
	cfgLink := filepath.Join(cfgDir, "bus")
	testutil.FailErr(t, "symlink cfgLink", os.Symlink(cfgSock, cfgLink))
	escapeLink := filepath.Join(shortDir, "to-cfg")
	testutil.FailErr(t, "symlink escapeLink", os.Symlink(cfgLink, escapeLink))

	sshLink := filepath.Join(shortDir, "to-ssh")
	testutil.FailErr(t, "symlink sshLink", os.Symlink(filepath.Join(sshDir, "agent.sock"), sshLink))

	envMap := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := m[k]
			return v, ok
		}
	}

	if got, ok := confine.ResolveCredentialStoreDBusSocket(envMap(map[string]string{
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + busSock + ",guid=0123456789abcdef0123456789abcdef",
	}), os.Getuid()); !ok || got != busSock {
		t.Fatalf("explicit unix:path with guid = (%q, %v), want (%q, true)", got, ok, busSock)
	}

	if got, ok := confine.ResolveCredentialStoreDBusSocket(envMap(map[string]string{
		"DBUS_SESSION_BUS_ADDRESS": "unix:abstract=/tmp/dbus-123;unix:path=" + busSock,
	}), os.Getuid()); !ok || got != busSock {
		t.Fatalf("fallback past abstract to unix:path = (%q, %v), want (%q, true)", got, ok, busSock)
	}

	if got, ok := confine.ResolveCredentialStoreDBusSocket(envMap(map[string]string{
		"XDG_RUNTIME_DIR": shortDir,
	}), os.Getuid()); !ok || got != busSock {
		t.Fatalf("XDG_RUNTIME_DIR fallback = (%q, %v), want (%q, true)", got, ok, busSock)
	}

	for name, env := range map[string]map[string]string{
		"abstract only":        {"DBUS_SESSION_BUS_ADDRESS": "unix:abstract=/tmp/dbus-only", "XDG_RUNTIME_DIR": shortDir},
		"tcp address":          {"DBUS_SESSION_BUS_ADDRESS": "tcp:host=127.0.0.1,port=1234", "XDG_RUNTIME_DIR": shortDir},
		"regular file":         {"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + regularFile},
		"control plane path":   {"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + cfgLink},
		"control plane target": {"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + escapeLink},
		"key material target":  {"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + sshLink},
		"system bus path":      {"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/dbus/system_bus_socket"},
		"relative runtime dir": {"XDG_RUNTIME_DIR": "relative/runtime"},
	} {
		if got, ok := confine.ResolveCredentialStoreDBusSocket(envMap(env), os.Getuid()); ok || got != "" {
			t.Errorf("%s: got (%q, %v), want (\"\", false)", name, got, ok)
		}
	}
}

func TestLinuxCredentialStoreReadRootsAndVerdicts(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))

	paths, err := detectionpack.BundledKeyMaterialPaths()
	testutil.FailErr(t, "load bundled key-material floor", err)
	confine.SetKeyMaterialPathsSource(func() []string { return paths })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(func() []string { return keyMaterialTestFloor }) })

	keyringsDir := filepath.Join(home, ".local", "share", "keyrings")
	kwalletDir := filepath.Join(home, ".local", "share", "kwalletd")
	sshDir := filepath.Join(home, ".ssh")
	for _, dir := range []string{keyringsDir, kwalletDir, sshDir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	loginKeyring := filepath.Join(keyringsDir, "login.keyring")
	kdeWallet := filepath.Join(kwalletDir, "kdewallet.kwl")
	sshKey := filepath.Join(sshDir, "id_ed25519")
	for _, p := range []string{loginKeyring, kdeWallet, sshKey} {
		testutil.FailErr(t, "write "+p, os.WriteFile(p, []byte("secret"), 0o600))
	}

	proj := fspath.CanonicalPath(t.TempDir())
	base := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDeny}
	ordinary := confine.BoundaryOf(&base)
	for _, p := range []string{loginKeyring, kdeWallet, sshKey} {
		if ordinary.Filesystem.Verdict(confine.AccessRead, p).Allowed {
			t.Fatalf("ordinary boundary allowed reading %s", p)
		}
	}

	withCred := base
	withCred.CredentialStore = true
	granted := confine.BoundaryOf(&withCred)
	for _, p := range []string{loginKeyring, kdeWallet} {
		if !granted.Filesystem.Verdict(confine.AccessRead, p).Allowed {
			t.Fatalf("credential_store boundary denied reading %s", p)
		}
		if granted.Filesystem.Verdict(confine.AccessWrite, p).Allowed {
			t.Fatalf("credential_store boundary allowed writing %s", p)
		}
	}
	if granted.Filesystem.Verdict(confine.AccessRead, sshKey).Allowed {
		t.Fatal("credential_store boundary allowed reading ~/.ssh/id_ed25519")
	}

	// Replacing ~/.local/share/kwalletd with a symlink to ~/.ssh must not admit ~/.ssh.
	testutil.FailErr(t, "remove kwalletDir", os.RemoveAll(kwalletDir))
	testutil.FailErr(t, "symlink kwalletd -> .ssh", os.Symlink(sshDir, kwalletDir))
	afterSymlink := confine.BoundaryOf(&withCred)
	if afterSymlink.Filesystem.Verdict(confine.AccessRead, sshKey).Allowed {
		t.Fatal("symlinked kwalletd admitted ~/.ssh/id_ed25519")
	}
}
