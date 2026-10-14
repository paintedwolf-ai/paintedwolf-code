package gitcredential

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func developmentVault(t *testing.T) vaultBackend {
	t.Helper()
	path := filepath.Join(t.TempDir(), credentialstore.VaultBasename)
	return vaultBackend{open: func() (*credentialstore.Store, error) {
		return credentialstore.OpenFile(credentialstore.Slot{Path: path, Namespace: credentialstore.NamespaceGitCredentials, Context: vaultContext}, nil)
	}}
}

func TestVaultBackendRoundTripsThroughTheHelperProtocol(t *testing.T) {
	store := developmentVault(t)
	var out bytes.Buffer
	testutil.FailErr(t, "store", runWithBackend("store", strings.NewReader("protocol=https\nhost=github.com\nusername=alice\npassword=tok en\n\n"), &out, store))
	testutil.FailErr(t, "get", runWithBackend("get", strings.NewReader("protocol=https\nhost=GitHub.com\n\n"), &out, store))
	if out.String() != "username=alice\npassword=tok en\n\n" {
		t.Fatalf("get output = %q", out.String())
	}
	out.Reset()
	testutil.FailErr(t, "get other user", runWithBackend("get", strings.NewReader("protocol=https\nhost=github.com\nusername=bob\n\n"), &out, store))
	if out.Len() != 0 {
		t.Fatalf("another username matched: %q", out.String())
	}
	testutil.FailErr(t, "get with path", runWithBackend("get", strings.NewReader("protocol=https\nhost=github.com\npath=org/repo.git\n\n"), &out, store))
	if !strings.Contains(out.String(), "username=alice") {
		t.Fatalf("host credential did not cover a path request: %q", out.String())
	}
	out.Reset()
	testutil.FailErr(t, "store path credential", runWithBackend("store", strings.NewReader("protocol=https\nhost=github.com\npath=org/repo.git\nusername=carol\npassword=p\n\n"), &out, store))
	testutil.FailErr(t, "get path credential", runWithBackend("get", strings.NewReader("protocol=https\nhost=github.com\npath=/org/repo.git/\n\n"), &out, store))
	if !strings.Contains(out.String(), "username=carol") {
		t.Fatalf("path credential was not preferred: %q", out.String())
	}
	out.Reset()
	testutil.FailErr(t, "erase other user", runWithBackend("erase", strings.NewReader("protocol=https\nhost=github.com\nusername=bob\n\n"), &out, store))
	testutil.FailErr(t, "get after mismatched erase", runWithBackend("get", strings.NewReader("protocol=https\nhost=github.com\n\n"), &out, store))
	if !strings.Contains(out.String(), "username=alice") {
		t.Fatalf("mismatched erase removed the credential: %q", out.String())
	}
	out.Reset()
	testutil.FailErr(t, "erase", runWithBackend("erase", strings.NewReader("protocol=https\nhost=github.com\nusername=alice\n\n"), &out, store))
	testutil.FailErr(t, "get after erase", runWithBackend("get", strings.NewReader("protocol=https\nhost=github.com\n\n"), &out, store))
	if out.Len() != 0 {
		t.Fatalf("erased credential still served: %q", out.String())
	}
	testutil.FailErr(t, "erase absent", runWithBackend("erase", strings.NewReader("protocol=https\nhost=absent.example\n\n"), &out, store))
}

func TestVaultBackendRequiresAnAddressableRemote(t *testing.T) {
	store := developmentVault(t)
	for _, request := range []string{"host=github.com\n\n", "protocol=https\n\n"} {
		if _, _, err := store.Get(mustParse(t, request)); err == nil {
			t.Fatalf("request %q resolved a key", request)
		}
		if err := store.Store(mustParse(t, request+"username=a\npassword=b\n")); err == nil {
			t.Fatalf("request %q was stored", request)
		}
		if err := store.Erase(mustParse(t, request)); err == nil {
			t.Fatalf("request %q was erased", request)
		}
	}
	keys, err := credentialKeys(Credential{Protocol: "HTTPS", Host: "Example.com:8443", Path: "/a/b/"})
	testutil.FailErr(t, "keys", err)
	if len(keys) != 2 || keys[0] != "https://example.com:8443/a/b" || keys[1] != "https://example.com:8443" {
		t.Fatalf("keys = %q", keys)
	}
}

func mustParse(t *testing.T, request string) Credential {
	t.Helper()
	credential, err := parse(strings.NewReader(request))
	testutil.FailErr(t, "parse request", err)
	return credential
}

func TestVaultListAndRemoteSubjects(t *testing.T) {
	store := developmentVault(t)
	testutil.FailErr(t, "store", store.Store(Credential{Protocol: "https", Host: "github.com", Username: "alice", Password: "secret"}))
	entries, err := store.List()
	testutil.FailErr(t, "list", err)
	if len(entries) != 1 || entries[0].Key != "https://github.com" || entries[0].Username != "alice" {
		t.Fatalf("entries = %+v", entries)
	}
	credential, err := CredentialForRemote("HTTPS://GitHub.com/org/repo.git/")
	testutil.FailErr(t, "remote subject", err)
	if credential.Protocol != "https" || credential.Host != "GitHub.com" || credential.Path != "org/repo.git" {
		t.Fatalf("subject = %+v", credential)
	}
	for _, remote := range []string{"", "github.com", "ssh://git@github.com/x", "git@github.com:org/repo.git", "https://"} {
		if _, err := CredentialForRemote(remote); err == nil {
			t.Fatalf("%q was accepted", remote)
		}
	}
}
