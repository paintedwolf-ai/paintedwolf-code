package sshproxy

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLookupKnownHostKeysAndHashedEntries(t *testing.T) {
	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "key1", err)
	signer1, err := ssh.NewSignerFromKey(priv1)
	testutil.FailErr(t, "signer1", err)
	pub1B64 := base64.StdEncoding.EncodeToString(signer1.PublicKey().Marshal())

	_, priv2, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "key2", err)
	signer2, err := ssh.NewSignerFromKey(priv2)
	testutil.FailErr(t, "signer2", err)
	pub2B64 := base64.StdEncoding.EncodeToString(signer2.PublicKey().Marshal())

	salt := []byte("0123456789abcdef0123")
	mac := hmac.New(sha1.New, salt)
	_, _ = mac.Write([]byte("[hashed.example]:2222"))
	hashedToken := "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	knownFile := filepath.Join(t.TempDir(), "known_hosts")
	content := strings.Join([]string{
		"# comment",
		"git.example,[git.example]:22 ssh-ed25519 " + pub1B64,
		"*.corp.example,!blocked.corp.example ssh-ed25519 " + pub1B64,
		"@revoked revoked.example ssh-ed25519 " + pub1B64,
		hashedToken + " ssh-ed25519 " + pub2B64,
	}, "\n") + "\n"
	testutil.FailErr(t, "write known_hosts", os.WriteFile(knownFile, []byte(content), 0o600))

	entries := LookupKnownHostKeys([]string{knownFile}, "git.example", 22)
	if len(entries) != 1 {
		t.Fatalf("git.example entries = %d, want 1", len(entries))
	}
	matched, conflicted := MatchesKnownHostKey(entries, "ssh-ed25519", signer1.PublicKey().Marshal())
	if !matched || conflicted {
		t.Fatalf("matching key reported matched=%v conflicted=%v", matched, conflicted)
	}
	matched, conflicted = MatchesKnownHostKey(entries, "ssh-ed25519", signer2.PublicKey().Marshal())
	if matched || !conflicted {
		t.Fatalf("conflicting key reported matched=%v conflicted=%v", matched, conflicted)
	}

	if got := LookupKnownHostKeys([]string{knownFile}, "blocked.corp.example", 22); len(got) != 0 {
		t.Fatalf("negated host matched: %+v", got)
	}
	if got := LookupKnownHostKeys([]string{knownFile}, "allowed.corp.example", 22); len(got) != 1 {
		t.Fatalf("wildcard host missed: %+v", got)
	}
	if got := LookupKnownHostKeys([]string{knownFile}, "revoked.example", 22); len(got) != 0 {
		t.Fatalf("revoked host matched: %+v", got)
	}
	hashedEntries := LookupKnownHostKeys([]string{knownFile}, "hashed.example", 2222)
	if len(hashedEntries) != 1 {
		t.Fatalf("hashed [host]:port missed: %+v", hashedEntries)
	}
}

func TestProbeSSHServerHostKey(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "host key", err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	testutil.FailErr(t, "host signer", err)

	cfg := &ssh.ServerConfig{NoClientAuth: false, PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _, _ = ssh.NewServerConn(conn, cfg)
	}()

	keyType, keyBlob, ok := ProbeSSHServerHostKey(t.Context(), ln.Addr().String())
	if !ok || keyType != hostSigner.PublicKey().Type() || string(keyBlob) != string(hostSigner.PublicKey().Marshal()) {
		t.Fatalf("ProbeSSHServerHostKey ok=%v type=%q", ok, keyType)
	}
}

func TestParseDirectSSHInvocation(t *testing.T) {
	home := t.TempDir()
	expKey := filepath.Join(home, ".ssh", "deploy_ed25519")
	port := uint16(2222)
	inv := ParseDirectSSHInvocation(
		[]string{"ssh", "-p", strconv.Itoa(int(port)), "-i", expKey, "-o", "StrictHostKeyChecking=yes", "git@example.com", "uname", "-a"},
		[]string{"SSH_AUTH_SOCK=/tmp/agent.sock"},
		home,
	)
	if inv.Host != "example.com" || inv.Port != port || inv.Destination != "example.com:2222" || inv.StrictHostKeyChecking != "yes" || inv.IdentityAgent != "/tmp/agent.sock" {
		t.Fatalf("parsed invocation = %+v", inv)
	}
	if !IsSSHTrustPath(filepath.Join(home, ".ssh", "known_hosts"), nil) || !IsSSHClientReadProbe(expKey) {
		t.Fatal("IsSSHTrustPath or IsSSHClientReadProbe returned false")
	}
}
