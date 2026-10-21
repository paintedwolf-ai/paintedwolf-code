package credentialstore

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

// unavailableCustody probes a socket path nothing serves. The path stays short
// so the dial fails as an absent socket, not as an overlong address.
func unavailableCustody(t *testing.T) custody {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pw-absent-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return probeCustody(vaultidentity.NewClient(filepath.Join(dir, "absent.sock")))
}

func TestServiceOutagePreservesCredentialsAndRecovers(t *testing.T) {
	_, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	healthy := probeCustody(client)
	path := filepath.Join(t.TempDir(), VaultBasename)
	backend := &namespaceBackend{vault: &vaultFile{path: path, provider: healthy.provider(path)}, namespace: "providers"}
	testutil.FailErr(t, "store credential", backend.Set("openai", "sk-kept"))
	original, err := os.ReadFile(path)
	testutil.FailErr(t, "read ciphertext", err)

	for _, outage := range []string{"socket disabled", "manifest unavailable", "launcher refused"} {
		t.Run(outage, func(t *testing.T) {
			lost := unavailableCustody(t)
			want := ErrCustodianAbsent
			if outage == "manifest unavailable" {
				_, badClient := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "manifest_unreadable"})
				lost = probeCustody(badClient)
			} else if outage == "launcher refused" {
				_, badClient := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusRefused, Code: "executable_mismatch"})
				lost = probeCustody(badClient)
				want = ErrLauncherRefused
			}
			setUnlockPassword(t, "a password cannot recover a service identity")
			defer ClearUnlockSecret()
			backend := &namespaceBackend{vault: &vaultFile{path: path, provider: lost.provider(path)}, namespace: "providers"}
			if _, err := backend.Snapshot(); !errors.Is(err, want) || errors.Is(err, ErrVaultCorrupt) {
				t.Fatalf("outage classification: %v", err)
			}
			current, err := os.ReadFile(path)
			testutil.FailErr(t, "read preserved ciphertext", err)
			if !bytes.Equal(original, current) {
				t.Fatal("outage changed ciphertext")
			}
			if _, err := os.Lstat(identityPath(path)); !os.IsNotExist(err) {
				t.Fatalf("outage created a wrapper: %v", err)
			}
			report := reportOf(t, lost, path)
			if outage != "launcher refused" && report.Mode != CustodyUnavailable {
				t.Fatalf("custody = %+v", report)
			}
		})
	}
	if got := readThrough(t, healthy, path); got != "sk-kept" {
		t.Fatalf("recovery = %q", got)
	}
}

func TestAbsentServiceSupportsOnlyNewOrWrappedPasswordVaults(t *testing.T) {
	lost := unavailableCustody(t)
	fresh := filepath.Join(t.TempDir(), VaultBasename)
	p := lost.provider(fresh)
	ClearUnlockSecret()
	if _, err := p.LoadOrCreate(false); !errors.Is(err, ErrVaultUninitialized) {
		t.Fatalf("first launch: %v", err)
	}
	path := wrappedVault(t, "correct horse battery staple")
	if got := readThrough(t, lost, path); got != "sk-kept" {
		t.Fatalf("password vault = %q", got)
	}
	if report := reportOf(t, lost, path); report.Mode != CustodyAppPassword {
		t.Fatalf("password custody = %+v", report)
	}
	if _, err := p.LoadOrCreate(false); err != nil {
		t.Fatalf("password setup: %v", err)
	}
}

func TestInterruptedMoveToPasswordRemainsRecoverable(t *testing.T) {
	_, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	healthy := probeCustody(client)
	path := wrappedVault(t, "correct horse battery staple")
	testutil.FailErr(t, "move to service", healthy.moveToService(path))
	host := healthy.serviceHost(path)
	doc, err := host.service.LoadOrCreate(true)
	testutil.FailErr(t, "load held identity", err)
	testutil.FailErr(t, "write verified wrapper before interruption", host.wrapped.commit(doc, configuredUnlockSecret()))
	if got := readThrough(t, unavailableCustody(t), path); got != "sk-kept" {
		t.Fatalf("interrupted password copy = %q", got)
	}
	testutil.FailErr(t, "finish move", healthy.moveToAppPassword(path))
	if got := readThrough(t, unavailableCustody(t), path); got != "sk-kept" {
		t.Fatalf("exported password copy = %q", got)
	}
	if reportOf(t, healthy, path).Mode != CustodyAppPassword {
		t.Fatal("re-enabled service silently took custody")
	}
}

// hangingUpService is a live socket whose server closes every connection: a
// service briefly failing, not one that is gone.
func hangingUpService(t *testing.T) *vaultidentity.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pw-hangup-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return vaultidentity.NewClient(socket)
}

func installedService(t *testing.T, installed bool) {
	t.Helper()
	previous := serviceExecutable
	serviceExecutable = filepath.Join(t.TempDir(), "pw-vault-identity")
	if installed {
		testutil.FailErr(t, "install service fixture", os.WriteFile(serviceExecutable, []byte("fixture"), 0o755))
	}
	t.Cleanup(func() { serviceExecutable = previous })
}

func TestCustodianAbsenceComesFromStructuredFacts(t *testing.T) {
	installedService(t, true)
	if c := unavailableCustody(t); c.mode != custodyUnavailable || !c.absent {
		t.Fatalf("no socket: %+v, want an absent custodian", c)
	}
	_, broken := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "manifest_unreadable"})
	if c := probeCustody(broken); !c.absent {
		t.Fatalf("service that cannot serve: %+v, want absent", c)
	}
	if c := probeCustody(hangingUpService(t)); c.mode != custodyUnavailable || c.absent {
		t.Fatalf("live socket failing: %+v, want briefly unreachable", c)
	}
	installedService(t, false)
	if c := probeCustody(hangingUpService(t)); !c.absent {
		t.Fatalf("live socket without the installed service: %+v, want absent", c)
	}
}

func TestAbsentServiceOffersResetWhileAnUnreachableOneRefusesIt(t *testing.T) {
	_, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	healthy := probeCustody(client)
	vault := func() (string, []byte) {
		path := filepath.Join(t.TempDir(), VaultBasename)
		backend := &namespaceBackend{vault: &vaultFile{path: path, provider: healthy.provider(path)}, namespace: "providers"}
		testutil.FailErr(t, "store credential", backend.Set("openai", "sk-kept"))
		original, err := os.ReadFile(path)
		testutil.FailErr(t, "read ciphertext", err)
		return path, original
	}

	installedService(t, true)
	path, original := vault()
	unreachable := probeCustody(hangingUpService(t))
	backend := &namespaceBackend{vault: &vaultFile{path: path, provider: unreachable.provider(path)}, namespace: "providers"}
	if _, err := backend.Snapshot(); !errors.Is(err, ErrCustodianUnavailable) || errors.Is(err, ErrCustodianAbsent) {
		t.Fatalf("briefly unreachable service: %v", err)
	}
	if err := (&vaultFile{path: path, provider: unreachable.provider(path)}).purge(); !errors.Is(err, ErrCustodianUnavailable) {
		t.Fatalf("purge with the service briefly unreachable: %v, want a refusal", err)
	}
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read preserved ciphertext", err)
	if !bytes.Equal(original, current) {
		t.Fatal("a refused purge changed the vault")
	}
	if got := readThrough(t, healthy, path); got != "sk-kept" {
		t.Fatalf("after the service returns = %q", got)
	}

	// Uninstalled: the person may reset, and a new vault opens.
	path, _ = vault()
	installedService(t, false)
	gone := unavailableCustody(t)
	backend = &namespaceBackend{vault: &vaultFile{path: path, provider: gone.provider(path)}, namespace: "providers"}
	if _, err := backend.Snapshot(); !errors.Is(err, ErrCustodianAbsent) {
		t.Fatalf("uninstalled service: %v, want an absent custodian", err)
	}
	testutil.FailErr(t, "reset with the service gone", (&vaultFile{path: path, provider: gone.provider(path)}).purge())
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("reset left the unopenable vault: %v", err)
	}

	// A vault the person moved to the app password does not depend on the
	// service, so it resets whatever the service's state.
	installedService(t, true)
	wrapped := wrappedVault(t, "correct horse battery staple")
	testutil.FailErr(t, "reset a password vault", (&vaultFile{path: wrapped, provider: unreachable.provider(wrapped)}).purge())
	if _, err := os.Lstat(wrapped); !os.IsNotExist(err) {
		t.Fatalf("password vault not reset: %v", err)
	}
}
