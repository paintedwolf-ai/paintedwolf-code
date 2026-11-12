package gitcredential

import "testing"

// A recipient covers a request only for its own user and, when Git sends a
// path, its own repository.
func TestRecipientCoversItsUserAndPath(t *testing.T) {
	r := Recipient{Protocol: "https", Host: "git.example", Username: "alice", Path: "org/repo.git"}
	cases := []struct {
		request Credential
		covered bool
	}{
		{Credential{Protocol: "https", Host: "git.example"}, true},
		{Credential{Protocol: "HTTPS", Host: "Git.Example", Username: "alice", Path: "/org/repo.git/"}, true},
		{Credential{Protocol: "https", Host: "git.example", Username: "mallory"}, false},
		{Credential{Protocol: "https", Host: "git.example", Path: "org/other.git"}, false},
		{Credential{Protocol: "http", Host: "git.example"}, false},
		{Credential{Protocol: "https", Host: "git.example:8443"}, false},
	}
	for _, c := range cases {
		if got := r.covers(c.request); got != c.covered {
			t.Fatalf("covers(%+v) = %v, want %v", c.request, got, c.covered)
		}
	}
}
