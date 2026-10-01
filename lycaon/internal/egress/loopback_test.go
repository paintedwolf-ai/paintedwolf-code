package egress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egress"
)

func TestLoopbackSpellingsAgree(t *testing.T) {
	literal := []string{
		"127.0.0.1", "127.0.0.1.", " 127.0.0.1 ", "127.0.0.2",
		"::1", "[::1]", "0:0:0:0:0:0:0:1", "::ffff:127.0.0.1", "[::ffff:127.0.0.1]",
	}
	for _, host := range literal {
		if !egress.LoopbackLiteral(host) {
			t.Errorf("LoopbackLiteral(%q) = false, want true", host)
		}
		if !egress.SyntacticLoopback(host) {
			t.Errorf("SyntacticLoopback(%q) = false, want true", host)
		}
	}

	// The reserved name is loopback by definition, but it is not an address.
	for _, host := range []string{"localhost", "LOCALHOST", "LocalHost", "localhost.", " localhost "} {
		if egress.LoopbackLiteral(host) {
			t.Errorf("LoopbackLiteral(%q) = true; a name is not an address literal", host)
		}
		if !egress.SyntacticLoopback(host) {
			t.Errorf("SyntacticLoopback(%q) = false, want true", host)
		}
	}

	for _, host := range []string{"", "example.com", "0.0.0.0", "db.localhost", "localhost.example.com", "notlocalhost"} {
		if egress.LoopbackLiteral(host) || egress.SyntacticLoopback(host) {
			t.Errorf("%q reported as loopback", host)
		}
	}
}

// Resolver-specific address spellings are not IP literals.
func TestSyntacticLoopbackIsNotAResolutionClaim(t *testing.T) {
	for _, host := range []string{"127.1", "127.000.000.001", "2130706433"} {
		if egress.SyntacticLoopback(host) {
			t.Fatalf("SyntacticLoopback(%q) = true — if this now parses, "+
				"say so in the doc comment; callers are told it is a spelling test only", host)
		}
	}
}
