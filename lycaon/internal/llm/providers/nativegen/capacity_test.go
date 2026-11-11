package nativegen

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"testing"
)

func TestRuntimeCapacityAliases(t *testing.T) {
	runtime := NewRuntime(RuntimeConfig{})
	defer runtime.Close()
	first := New("first", Options{Runtime: runtime})
	alias := New("alias", Options{Runtime: runtime})
	if modelcall.CapacityKey(first) != modelcall.CapacityKey(alias) {
		t.Fatal("aliases do not share runtime capacity")
	}
	other := NewRuntime(RuntimeConfig{})
	defer other.Close()
	if modelcall.CapacityKey(first) == modelcall.CapacityKey(New("first", Options{Runtime: other})) {
		t.Fatal("independent runtimes share capacity")
	}
	if New("missing", Options{}).CapacityGroup() != nil {
		t.Fatal("missing runtime has capacity")
	}
}
