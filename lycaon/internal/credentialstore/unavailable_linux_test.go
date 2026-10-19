package credentialstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func unavailableCustody(t *testing.T) custody {
	t.Helper()
	return probeCustody(vaultidentity.NewClient(filepath.Join(t.TempDir(), "absent.sock")))
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
			want := ErrCustodianUnavailable
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
