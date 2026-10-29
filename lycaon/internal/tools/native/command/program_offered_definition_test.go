package command

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// A native writer's child is validated against the definition the program
// offers; the write door keeps the definition redirects use, progress rule
// included, while other registry tools carry the program's path capabilities.
func TestOfferedProgramDefinitionKeepsTheWriteDoor(t *testing.T) {
	compile := func(name string) toolcontract.OperationDefinition {
		t.Helper()
		contract, ok := toolcontract.Lookup(name)
		if !ok {
			t.Fatalf("no %s contract", name)
		}
		definition, err := toolcontract.ToolOperation(name, json.RawMessage(`{"type":"object"}`), contract)
		if err != nil {
			t.Fatalf("compile %s: %v", name, err)
		}
		return definition
	}
	write := compile("write")
	offered := offeredProgramDefinition("write", write)
	if offered.Digest() != write.Digest() || offered.Facts().ProgressGate != write.Facts().ProgressGate {
		t.Fatalf("write offered as %+v, want its own definition", offered.Facts())
	}
	read := compile("read")
	program := offeredProgramDefinition("read", read)
	if program.Digest() != programOperationDefinition(read).Digest() || program.Digest() == read.Digest() {
		t.Fatal("registry tool not offered with the program definition")
	}
	if program.Facts().Contract.Capabilities&toolcontract.CapabilityWriteRoot == 0 {
		t.Fatal("program definition lost its path capabilities")
	}
}
