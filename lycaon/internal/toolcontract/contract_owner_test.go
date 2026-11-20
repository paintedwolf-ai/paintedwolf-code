package toolcontract

import "testing"

// Isolation is a boundary every owner passes through, never an owner itself:
// no compiled contract names a generic boundary as its subsystem owner.
func TestIsolationCannotBecomeASubsystemOwner(t *testing.T) {
	if len(compiledContracts) == 0 {
		t.Fatal("no compiled contracts")
	}
	for name := range compiledContracts {
		contract, ok := Lookup(name)
		if !ok || contract.Owner == "" {
			t.Errorf("%s has no subsystem owner", name)
			continue
		}
		switch contract.Owner {
		case "isolation", "sandbox", "confine", "confinement":
			t.Errorf("%s declares generic boundary %q as its subsystem owner", name, contract.Owner)
		}
	}
}
