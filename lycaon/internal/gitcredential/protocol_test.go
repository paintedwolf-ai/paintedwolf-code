package gitcredential

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type memoryBackend struct {
	credential Credential
	stored     Credential
	erased     Credential
}

func (b *memoryBackend) Get(Credential) (Credential, bool, error) {
	return b.credential, b.credential.Username != "", nil
}

func (b *memoryBackend) Store(credential Credential) error {
	b.stored = credential
	return nil
}

func (b *memoryBackend) Erase(credential Credential) error {
	b.erased = credential
	return nil
}

func TestGetWritesGitCredentialProtocol(t *testing.T) {
	backend := &memoryBackend{credential: Credential{Username: "octocat", Password: "token"}}
	var out bytes.Buffer
	testutil.FailErr(t, "get", runWithBackend(
		"get", strings.NewReader("protocol=https\nhost=github.com\n\n"), &out, backend,
	))
	if got := out.String(); got != "username=octocat\npassword=token\n\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestStoreRequiresCompleteCredential(t *testing.T) {
	backend := &memoryBackend{}
	request := "protocol=https\nhost=github.com\nusername=octocat\npassword=token\n\n"
	testutil.FailErr(t, "store", runWithBackend("store", strings.NewReader(request), io.Discard, backend))
	if backend.stored.Host != "github.com" || backend.stored.Password != "token" {
		t.Fatalf("stored = %+v", backend.stored)
	}
}

func TestHostPortAndProtocolMapping(t *testing.T) {
	host, port := splitHostPort("github.example:8443")
	if host != "github.example" || port != 8443 {
		t.Fatalf("host/port = %q/%d", host, port)
	}
	got, err := keychainProtocol("https")
	testutil.FailErr(t, "map protocol", err)
	if got != "htps" {
		t.Fatalf("protocol = %q", got)
	}
}
