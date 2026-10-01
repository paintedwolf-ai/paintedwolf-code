package secretharvest_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretharvest"
)

// Mint output can carry an unlabelled token beyond catalog context.
func TestMintedCandidatesFindsATokenNoRuleCouldReach(t *testing.T) {
	const token = "67ff6e39282cb4d81f8da08b44df3e8b524a5960"
	out := `{"stages":[{"command":"gitea admin user generate-access-token --username releasebot ` +
		`--token-name release-cli --scopes all","exit_code":0}],` +
		`"tail":"stdout: Access token was successfully created: ` + token + `\n","ok":true}`

	got := secretharvest.MintedCandidates(out)
	if !containsValue(got, token) {
		t.Fatalf("minted token not extracted from its own creating call: %v", got)
	}
}

// Ordinary prose, paths, and versions do not become evidence.
func TestMintedCandidatesIgnoresOrdinaryOutput(t *testing.T) {
	for _, out := range []string{
		"Compiled 42 packages in 3.2s",
		"/Users/someone/git/github/project/internal/session/manager_impl.go",
		"warning: this configuration option is deprecated and will be removed",
		"https://example.test/some/reasonably/long/path/segment/here",
		"v1.24.3-rc.1+build.20260820",
	} {
		if got := secretharvest.MintedCandidates(out); len(got) != 0 {
			t.Errorf("ordinary output yielded candidates %v: %q", got, out)
		}
	}
}

// One output has a fixed candidate bound.
func TestMintedCandidatesAreBounded(t *testing.T) {
	var b strings.Builder
	for i := range 100 {
		b.WriteString("tok")
		b.WriteString(strings.Repeat("a1b2c3", 4))
		b.WriteByte(byte('a' + i%26))
		b.WriteString("\n")
	}
	if got := len(secretharvest.MintedCandidates(b.String())); got > 16 {
		t.Errorf("candidates = %d, want the per-output bound", got)
	}
}

// Remembered values retain their minting rule.
func TestRememberMintedKeepsProvenance(t *testing.T) {
	values := secretharvest.RememberMinted(
		"credential-minting.gitea", "Mint a Gitea access token", "command",
		"Access token was successfully created: 67ff6e39282cb4d81f8da08b44df3e8b524a5960")
	if len(values) != 1 {
		t.Fatalf("values = %+v", values)
	}
	if values[0].RuleID != "credential-minting.gitea" || values[0].Title == "" {
		t.Errorf("provenance lost: %+v", values[0])
	}
	if !strings.Contains(values[0].Origin, "command") {
		t.Errorf("origin does not name the surface: %q", values[0].Origin)
	}
}

func containsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
