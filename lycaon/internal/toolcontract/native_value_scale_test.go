package toolcontract

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNativeValueWideReviewUsesPayloadAndStructure(t *testing.T) {
	limits, err := CatalogProgramMemoryLimits()
	if err != nil {
		t.Fatalf("read limits: %v", err)
	}
	memory, err := NewProgramMemory(limits)
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	paths := make([]string, 16384)
	for i := range paths {
		paths[i] = fmt.Sprintf("internal/%s/%05d.go", strings.Repeat("a", 32), i)
	}
	args := map[string]any{"operands": paths, "path_targets": map[string]any{"read": paths, "write": []string{}}}
	value, release, err := NewNativeValue(t.Context(), memory, OperationArgumentsDomain, args)
	if err != nil {
		t.Fatalf("encode wide review: %v", err)
	}
	decoded, releaseDecoded, err := value.Value(t.Context(), memory)
	if err != nil {
		release()
		t.Fatalf("decode wide review: %v", err)
	}
	got := decoded.(map[string]any)["operands"].([]any)
	if len(got) != len(paths) || !reflect.DeepEqual(got[0], paths[0]) || got[len(got)-1] != paths[len(paths)-1] {
		t.Fatal("wide review changed arguments")
	}
	if peak := memory.Peak().State; peak > 32<<20 {
		t.Fatalf("review peak %d exceeds structural bound", peak)
	}
	releaseDecoded()
	release()
	if used := memory.InUse(); used != (ProgramMemoryUsage{}) {
		t.Fatalf("review retained memory: %+v", used)
	}
}
