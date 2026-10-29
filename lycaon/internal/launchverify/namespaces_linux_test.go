//go:build linux

package launchverify

import (
	"os"
	"strings"
	"testing"
)

func TestReadCurrentNamespaceDoesNotAttestProtectedTransition(t *testing.T) {
	initial, err := ReadNamespace("/proc", 0)
	if err != nil {
		t.Fatalf("read verifier namespace: %v", err)
	}
	current, err := ReadNamespace("/proc", os.Getpid())
	if err != nil {
		t.Fatalf("read process namespace: %v", err)
	}
	if !sameNamespace(initial, current) {
		t.Fatal("current process did not retain verifier namespace identity")
	}
	if err := ProtectedNamespacePair(initial, current, current); err == nil {
		t.Fatal("ordinary process attested its original namespace as a protected transition")
	}
}

func TestNamespaceCredentialFieldsAreMandatoryAndUnique(t *testing.T) {
	valid := "CapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapInh:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\n"
	facts, err := namespaceCredentials(NamespaceFacts{}, valid)
	if err != nil || !facts.NoNewPrivileges || facts.Capabilities != [4]uint64{} {
		t.Fatalf("read sealed credentials: %+v %v", facts, err)
	}
	for _, raw := range []string{strings.Replace(valid, "NoNewPrivs:\t1\n", "", 1), valid + "CapEff: 0\n", strings.Replace(valid, "NoNewPrivs:\t1", "NoNewPrivs:\t2", 1), strings.Replace(valid, "CapAmb:\t0000000000000000", "CapAmb:\tnone", 1)} {
		if _, err := namespaceCredentials(NamespaceFacts{}, raw); err == nil {
			t.Error("accepted incomplete or ambiguous credential facts")
		}
	}
}
