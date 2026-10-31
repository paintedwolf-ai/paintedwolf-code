package confine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialStoreSeatbeltProfileAndVerdicts(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	keychainsDir := filepath.Join(home, "Library", "Keychains")
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "mkdir keychains", os.MkdirAll(keychainsDir, 0o700))
	testutil.FailErr(t, "mkdir ssh", os.MkdirAll(sshDir, 0o700))

	loginDB := filepath.Join(keychainsDir, "login.keychain-db")
	loginLegacy := filepath.Join(keychainsDir, "login.keychain")
	otherDB := filepath.Join(keychainsDir, "other.keychain-db")
	sshKey := filepath.Join(sshDir, "id_ed25519")
	for _, p := range []string{loginDB, otherDB, sshKey} {
		testutil.FailErr(t, "write "+p, os.WriteFile(p, []byte("secret"), 0o600))
	}
	// A symlink escaping ~/Library/Keychains must not admit its external target.
	testutil.FailErr(t, "symlink login.keychain -> sshKey", os.Symlink(sshKey, loginLegacy))

	proj := fspath.CanonicalPath(t.TempDir())
	base := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDeny}
	ordinary, err := confine.BuildSeatbeltProfile(base)
	testutil.FailErr(t, "ordinary profile", err)
	if strings.Contains(ordinary, loginDB) {
		t.Fatalf("ordinary profile admitted login.keychain-db:\n%s", ordinary)
	}
	ordinaryBoundary := confine.BoundaryOf(&base)
	if ordinaryBoundary.Filesystem.Verdict(confine.AccessRead, loginDB).Allowed {
		t.Fatal("ordinary boundary allowed reading login.keychain-db")
	}

	withCred := base
	withCred.CredentialStore = true
	profile, err := confine.BuildSeatbeltProfile(withCred)
	testutil.FailErr(t, "credential_store profile", err)
	if !strings.Contains(profile, `(literal `+ sbplQuote(loginDB) +`)`) {
		t.Fatalf("profile missing literal login.keychain-db:\n%s", profile)
	}
	if !strings.Contains(profile, "(allow file-read-metadata\n") || !strings.Contains(profile, `(literal `+ sbplQuote(keychainsDir) +`)`) {
		t.Fatalf("profile missing file-read-metadata for Keychains dir:\n%s", profile)
	}
	if strings.Contains(profile, sshKey) {
		t.Fatalf("profile admitted symlink target outside Keychains dir:\n%s", profile)
	}

	b := confine.BoundaryOf(&withCred)
	if !b.CredentialStore {
		t.Fatal("boundary did not record CredentialStore")
	}
	if !b.Filesystem.Verdict(confine.AccessRead, loginDB).Allowed {
		t.Fatal("credential_store boundary denied reading login.keychain-db")
	}
	if b.Filesystem.Verdict(confine.AccessRead, keychainsDir).Allowed {
		t.Fatal("credential_store boundary allowed listing ~/Library/Keychains")
	}
	if b.Filesystem.Verdict(confine.AccessRead, otherDB).Allowed {
		t.Fatal("credential_store boundary allowed reading sibling keychain")
	}
	if b.Filesystem.Verdict(confine.AccessRead, sshKey).Allowed {
		t.Fatal("credential_store boundary allowed reading ~/.ssh/id_ed25519")
	}
	if b.Filesystem.Verdict(confine.AccessWrite, loginDB).Allowed {
		t.Fatal("credential_store boundary allowed writing login.keychain-db")
	}
	if b.Filesystem.Verdict(confine.AccessWrite, keychainsDir).Allowed {
		t.Fatal("credential_store boundary allowed writing ~/Library/Keychains")
	}
}

func TestCredentialStoreSeatbeltLiveLoginKeychainReadOnly(t *testing.T) {
	self := requireSeatbelt(t)
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	keychainsDir := filepath.Join(home, "Library", "Keychains")
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "mkdir keychains", os.MkdirAll(keychainsDir, 0o700))
	testutil.FailErr(t, "mkdir ssh", os.MkdirAll(sshDir, 0o700))

	loginDB := filepath.Join(keychainsDir, "login.keychain-db")
	otherDB := filepath.Join(keychainsDir, "other.keychain-db")
	sshKey := filepath.Join(sshDir, "id_ed25519")
	testutil.FailErr(t, "write login.keychain-db", os.WriteFile(loginDB, []byte("login-db-payload"), 0o600))
	testutil.FailErr(t, "write other.keychain-db", os.WriteFile(otherDB, []byte("other-db-payload"), 0o600))
	testutil.FailErr(t, "write id_ed25519", os.WriteFile(sshKey, []byte("ssh-key-payload"), 0o600))

	proj := fspath.CanonicalPath(t.TempDir())
	base := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDeny}
	if code := confinedExit(t, self, base, "/bin/cat", loginDB); code == 0 {
		t.Fatal("ordinary sandbox read login.keychain-db")
	}

	withCred := base
	withCred.CredentialStore = true
	code, stdout, stderr := confinedStdout(t, self, withCred, "/bin/cat", loginDB)
	if code != 0 || stdout != "login-db-payload" {
		t.Fatalf("read login.keychain-db under credential_store: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if code := confinedExit(t, self, withCred, "/bin/test", "-d", keychainsDir); code != 0 {
		t.Fatalf("stat ~/Library/Keychains failed under credential_store: exit %d", code)
	}
	if code := confinedExit(t, self, withCred, "/bin/ls", keychainsDir); code == 0 {
		t.Fatal("credential_store allowed directory listing of ~/Library/Keychains")
	}
	if code := confinedExit(t, self, withCred, "/bin/cat", otherDB); code == 0 {
		t.Fatal("credential_store allowed reading sibling other.keychain-db")
	}
	if code := confinedExit(t, self, withCred, "/bin/cat", sshKey); code == 0 {
		t.Fatal("credential_store allowed reading ~/.ssh/id_ed25519")
	}
	if code := confinedExit(t, self, withCred, "/usr/bin/touch", loginDB); code == 0 {
		t.Fatal("credential_store allowed mutating login.keychain-db")
	}
	if code := confinedExit(t, self, withCred, "/usr/bin/touch", filepath.Join(keychainsDir, "injected.keychain-db")); code == 0 {
		t.Fatal("credential_store allowed creating a file inside ~/Library/Keychains")
	}
}

func sbplQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
