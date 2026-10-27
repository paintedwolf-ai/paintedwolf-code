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
			switch outage {
			case "manifest unavailable":
				_, badClient := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "manifest_unreadable"})
				lost = probeCustody(badClient)
			case "launcher refused":
				_, badClient := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusRefused, Code: "executable_mismatch"})
				lost = probeCustody(badClient)
				want = ErrLauncherRefused
			}
			backend := &namespaceBackend{vault: &vaultFile{path: path, provider: lost.provider(path)}, namespace: "providers"}
			if _, err := backend.Snapshot(); !errors.Is(err, want) || errors.Is(err, ErrVaultCorrupt) {
				t.Fatalf("outage classification: %v", err)
			}
			current, err := os.ReadFile(path)
			testutil.FailErr(t, "read preserved ciphertext", err)
			if !bytes.Equal(original, current) {
				t.Fatal("outage changed ciphertext")
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(path), "credential-vault-identity.age")); !os.IsNotExist(err) {
				t.Fatalf("outage created a wrapper: %v", err)
			}
			report, err := lost.report(path)
			testutil.FailErr(t, "report outage custody", err)
			if outage != "launcher refused" && report.Mode != CustodyUnavailable {
				t.Fatalf("custody = %+v", report)
			}
		})
	}
	if got := readThrough(t, healthy, path); got != "sk-kept" {
		t.Fatalf("recovery = %q", got)
	}
}

func TestUnavailableServiceNeverCreatesAnAlternateIdentity(t *testing.T) {
	installedService(t, true)
	for name, lost := range map[string]custody{
		"absent":      unavailableCustody(t),
		"unreachable": probeCustody(hangingUpService(t)),
		"unknown":     {},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, VaultBasename)
			if _, err := lost.provider(path).LoadOrCreate(false); !errors.Is(err, ErrCustodianAbsent) && !errors.Is(err, ErrCustodianUnavailable) {
				t.Fatalf("missing service did not refuse identity creation: %v", err)
			}
			entries, err := os.ReadDir(dir)
			testutil.FailErr(t, "read unchanged config root", err)
			if len(entries) != 0 {
				t.Fatal("missing service created a credential artifact")
			}
			report, err := lost.report(path)
			testutil.FailErr(t, "report unavailable custody", err)
			if report.Mode != CustodyUnavailable || report.ServiceVerified {
				t.Fatalf("unavailable custody = %+v", report)
			}
		})
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

func TestServiceOutageCannotPurgeRecoverableCredentials(t *testing.T) {
	_, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	healthy := probeCustody(client)
	path := filepath.Join(t.TempDir(), VaultBasename)
	backend := &namespaceBackend{vault: &vaultFile{path: path, provider: healthy.provider(path)}, namespace: "providers"}
	testutil.FailErr(t, "store credential", backend.Set("openai", "sk-kept"))
	original, err := os.ReadFile(path)
	testutil.FailErr(t, "read ciphertext", err)
	for _, lost := range []custody{unavailableCustody(t), probeCustody(hangingUpService(t))} {
		if err := (&vaultFile{path: path, provider: lost.provider(path)}).purge(); err == nil {
			t.Fatal("an unavailable custodian allowed credential removal")
		}
		current, err := os.ReadFile(path)
		testutil.FailErr(t, "read preserved ciphertext", err)
		if !bytes.Equal(original, current) {
			t.Fatal("a refused purge changed credentials")
		}
	}
	if got := readThrough(t, healthy, path); got != "sk-kept" {
		t.Fatalf("service recovery = %q", got)
	}
}

func readThrough(t *testing.T, c custody, path string) string {
	t.Helper()
	backend := &namespaceBackend{vault: &vaultFile{path: path, provider: c.provider(path)}, namespace: "providers"}
	snapshot, err := backend.Snapshot()
	testutil.FailErr(t, "read credential", err)
	return snapshot.Values["openai"]
}
