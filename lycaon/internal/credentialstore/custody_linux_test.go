package credentialstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

// wrappedVault writes a vault whose identity the app password wraps.
func wrappedVault(t *testing.T, password string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), VaultBasename)
	setUnlockPassword(t, password)
	t.Cleanup(ClearUnlockSecret)
	vault := &vaultFile{path: path, provider: &passphraseIdentityProvider{path: identityPath(path)}}
	backend := &namespaceBackend{vault: vault, namespace: "providers"}
	testutil.FailErr(t, "store a credential", backend.Set("openai", "sk-kept"))
	return path
}

// readThrough opens the vault afresh with the provider custody selects.
func readThrough(t *testing.T, c custody, path string) string {
	t.Helper()
	backend := &namespaceBackend{vault: &vaultFile{path: path, provider: c.provider(path)}, namespace: "providers"}
	snapshot, err := backend.Snapshot()
	testutil.FailErr(t, "read the vault", err)
	return snapshot.Values["openai"]
}

func reportOf(t *testing.T, c custody, path string) CustodyReport {
	t.Helper()
	report, err := c.report(path)
	testutil.FailErr(t, "report custody", err)
	return report
}

func TestWrappedVaultMovesIntoTheServiceAndBack(t *testing.T) {
	svc, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	c := probeCustody(client)
	path := wrappedVault(t, "correct horse battery staple")

	if r := reportOf(t, c, path); r.Mode != CustodyAppPassword || !r.ServiceVerified {
		t.Fatalf("wrapped vault report = %+v", r)
	}
	if got := c.provider(path).Describe(); got != "app password" {
		t.Fatalf("wrapped vault description = %q", got)
	}
	if readThrough(t, c, path) != "sk-kept" {
		t.Fatal("the wrapped vault did not open with the app password")
	}

	setUnlockPassword(t, "not the password at all")
	if err := c.moveToService(path); CustodyErrorCode(err) != "password_incorrect" {
		t.Fatalf("wrong password: %v", err)
	}
	ClearUnlockSecret()
	if err := c.moveToService(path); CustodyErrorCode(err) != "password_required" {
		t.Fatalf("no password: %v", err)
	}
	if len(svc.values) != 0 {
		t.Fatal("a refused move stored an identity")
	}

	setUnlockPassword(t, "correct horse battery staple")
	testutil.FailErr(t, "move into the service", c.moveToService(path))
	if _, err := os.Stat(identityPath(path)); !os.IsNotExist(err) {
		t.Fatalf("the wrapped identity remains: %v", err)
	}
	if r := reportOf(t, c, path); r.Mode != CustodyIdentityService {
		t.Fatalf("moved vault report = %+v", r)
	}
	ClearUnlockSecret()
	if readThrough(t, c, path) != "sk-kept" {
		t.Fatal("the service identity does not open the vault")
	}
	if err := c.moveToService(path); CustodyErrorCode(err) != "unchanged" {
		t.Fatalf("second move into the service: %v", err)
	}

	setUnlockPassword(t, "short")
	if err := c.moveToAppPassword(path); CustodyErrorCode(err) != "password_invalid" {
		t.Fatalf("short password: %v", err)
	}
	setUnlockPassword(t, "a new app password here")
	testutil.FailErr(t, "move to the app password", c.moveToAppPassword(path))
	if len(svc.values) != 0 {
		t.Fatal("the service still holds the identity")
	}
	if r := reportOf(t, c, path); r.Mode != CustodyAppPassword {
		t.Fatalf("returned vault report = %+v", r)
	}
	if readThrough(t, c, path) != "sk-kept" {
		t.Fatal("the new app password does not open the vault")
	}
	if err := c.moveToAppPassword(path); CustodyErrorCode(err) != "unchanged" {
		t.Fatalf("second move to the app password: %v", err)
	}
}

func TestInterruptedMoveIntoTheServiceCompletes(t *testing.T) {
	svc, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	c := probeCustody(client)
	path := wrappedVault(t, "correct horse battery staple")
	doc, err := (&passphraseIdentityProvider{path: identityPath(path)}).LoadOrCreate(true)
	testutil.FailErr(t, "unwrap", err)
	raw, err := encodeIdentityDocument(doc)
	testutil.FailErr(t, "encode", err)
	svc.values[identityAccount(path)] = raw

	if r := reportOf(t, c, path); r.Mode != CustodyIdentityService {
		t.Fatalf("both copies report = %+v", r)
	}
	testutil.FailErr(t, "finish the move", c.moveToService(path))
	if _, err := os.Stat(identityPath(path)); !os.IsNotExist(err) {
		t.Fatal("the wrapped identity remains")
	}
}

func TestMoveRefusesAnIdentityThatDoesNotOpenTheVault(t *testing.T) {
	svc, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	c := probeCustody(client)
	path := wrappedVault(t, "correct horse battery staple")
	stranger, err := generateIdentityDocument()
	testutil.FailErr(t, "generate", err)
	raw, err := encodeIdentityDocument(stranger)
	testutil.FailErr(t, "encode", err)
	svc.values[identityAccount(path)] = raw

	if err := c.moveToService(path); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("service holds another identity: %v", err)
	}
	if _, err := os.Stat(identityPath(path)); err != nil {
		t.Fatalf("the wrapped identity was removed: %v", err)
	}
	setUnlockPassword(t, "a new app password here")
	if err := c.moveToAppPassword(path); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("moving a stranger identity out: %v", err)
	}
	if identityOpensVault(path, stranger) == nil {
		t.Fatal("a stranger identity opened the vault")
	}
}

func TestCustodyWithoutAVerifyingServiceCannotMove(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	password := custody{mode: custodyPassword}
	if r := reportOf(t, password, path); r.Mode != CustodyAppPassword || r.ServiceVerified {
		t.Fatalf("password custody report = %+v", r)
	}
	if err := password.moveToService(path); !errors.Is(err, ErrServiceUnverified) {
		t.Fatalf("move without a service: %v", err)
	}
	refused := custody{mode: custodyRefused, refusal: errors.New("executable_mismatch")}
	if r := reportOf(t, refused, path); r.Mode != CustodyRefused || r.Detail == "" {
		t.Fatalf("refused custody report = %+v", r)
	}
	setUnlockPassword(t, "a new app password here")
	t.Cleanup(ClearUnlockSecret)
	if err := refused.moveToAppPassword(path); !errors.Is(err, ErrLauncherRefused) {
		t.Fatalf("move by a refused engine: %v", err)
	}
}

func TestServiceHostReportsUnreadableCustody(t *testing.T) {
	svc, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	c := probeCustody(client)
	path := filepath.Join(t.TempDir(), VaultBasename)
	svc.probe = vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "store"}
	if _, err := c.report(path); vaultidentity.StatusOf(err) != vaultidentity.StatusUnavailable {
		t.Fatalf("report during store failure: %v", err)
	}
	if c.provider(path).Describe() == "" {
		t.Fatal("no description during store failure")
	}
	setUnlockPassword(t, "a new app password here")
	t.Cleanup(ClearUnlockSecret)
	if err := c.moveToAppPassword(path); vaultidentity.StatusOf(err) != vaultidentity.StatusUnavailable {
		t.Fatalf("move during store failure: %v", err)
	}
}

func TestMissingWrappedIdentityIsNotCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	_, err := custody{mode: custodyPassword}.provider(path).LoadOrCreate(true)
	if !errors.Is(err, ErrIdentityMissing) || errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("missing wrapped identity: %v", err)
	}
}
