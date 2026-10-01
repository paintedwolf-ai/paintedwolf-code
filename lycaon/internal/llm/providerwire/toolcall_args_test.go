package providerwire

import (
	"encoding/json"
	"testing"
)

func TestWireToolArgsNilIsEmptyObject(t *testing.T) {
	got := ToolArgs(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestToolCallArgumentsJSONNilIsObject(t *testing.T) {
	if got := ToolCallArgumentsJSON(nil); got != "{}" {
		t.Fatalf("got %q want {}", got)
	}
}

func TestToolCallArgumentsJSONPreservesKeys(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal([]byte(ToolCallArgumentsJSON(map[string]any{"path": "a.txt"})), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["path"] != "a.txt" {
		t.Fatalf("got %#v", got)
	}
}
