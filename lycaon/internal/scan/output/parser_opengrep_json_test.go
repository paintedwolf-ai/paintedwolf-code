package output

import "testing"

func TestCanonicalOpengrepRuleID(t *testing.T) {
	in := "Users.example.work.project.config.scanners.rules.vendor.0xdea.rules.generic.raptor-bad-words"
	want := "opengrep:0xdea.rules.generic.raptor-bad-words"
	if got := canonicalOpengrepRuleID(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := canonicalOpengrepRuleID("lycaon.go.insecure-tls"); got != "opengrep:lycaon.go.insecure-tls" {
		t.Fatalf("short id = %q", got)
	}
}
