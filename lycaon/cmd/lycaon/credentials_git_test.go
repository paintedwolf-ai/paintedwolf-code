package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/testutil"
)

type memoryGitCredentials struct {
	entries map[string]gitcredential.Credential
}

func (m *memoryGitCredentials) List() ([]gitcredential.Entry, error) {
	entries := []gitcredential.Entry{}
	for key, credential := range m.entries {
		entries = append(entries, gitcredential.Entry{Key: key, Username: credential.Username})
	}
	return entries, nil
}

func (m *memoryGitCredentials) Store(credential gitcredential.Credential) error {
	m.entries[credential.Protocol+"://"+credential.Host] = credential
	return nil
}

func (m *memoryGitCredentials) Erase(credential gitcredential.Credential) error {
	delete(m.entries, credential.Protocol+"://"+credential.Host)
	return nil
}

func TestGitCredentialsCLIStoresListsAndRemoves(t *testing.T) {
	memory := &memoryGitCredentials{entries: map[string]gitcredential.Credential{}}
	previous := gitCredentialStore
	gitCredentialStore = func() gitCredentialBackend { return memory }
	t.Cleanup(func() { gitCredentialStore = previous })
	var out bytes.Buffer
	testutil.FailErr(t, "set", runGitCredentials([]string{"set", "https://github.com", "--username", "alice"}, strings.NewReader("ghp_token\n"), &out))
	stored := memory.entries["https://github.com"]
	if stored.Username != "alice" || stored.Password != "ghp_token" || stored.Host != "github.com" {
		t.Fatalf("stored = %+v", stored)
	}
	testutil.FailErr(t, "list", runGitCredentials([]string{"list"}, strings.NewReader(""), &out))
	if out.String() != "https://github.com\talice\n" || strings.Contains(out.String(), "ghp_token") {
		t.Fatalf("list = %q", out.String())
	}
	testutil.FailErr(t, "remove", runGitCredentials([]string{"remove", "https://github.com/"}, strings.NewReader(""), &out))
	if len(memory.entries) != 0 {
		t.Fatalf("entries after remove = %v", memory.entries)
	}
	for name, args := range map[string][]string{
		"no command":       {},
		"unknown":          {"forget"},
		"set without user": {"set", "https://github.com"},
		"set two remotes":  {"set", "https://github.com", "https://other.example", "--username", "a"},
		"set stray flag":   {"set", "https://github.com", "--token", "x", "--username", "a"},
		"set bad remote":   {"set", "github.com", "--username", "a"},
		"set ssh remote":   {"set", "ssh://github.com", "--username", "a"},
		"remove arity":     {"remove"},
		"remove relative":  {"remove", "github.com"},
	} {
		if err := runGitCredentials(args, strings.NewReader("token\n"), &out); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if err := runGitCredentials([]string{"set", "https://github.com", "--username", "alice"}, strings.NewReader("\n"), &out); err == nil {
		t.Fatal("empty token was stored")
	}
	t.Setenv("LYCAON_CREDENTIAL_UNLOCK_STDIN", "1")
	frame := append([]byte{0, 0, 0, 8}, []byte("password")...)
	testutil.FailErr(t, "set with unlock frame", runGitCredentials([]string{"set", "--username=bob", "https://example.com"}, bytes.NewReader(append(frame, []byte("tok\n")...)), &out))
	if memory.entries["https://example.com"].Password != "tok" {
		t.Fatalf("framed set stored %+v", memory.entries["https://example.com"])
	}
	if err := runGitCredentials([]string{"set", "https://example.com", "--username", "bob"}, strings.NewReader("short"), &out); err == nil {
		t.Fatal("truncated unlock frame was accepted")
	}
}
