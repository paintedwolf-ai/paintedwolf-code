package captureprojection

import (
	"fmt"
	"testing"
)

func TestValueWideProjectionTracksPayloadAndContainers(t *testing.T) {
	projector := testProjector(t, nil)
	options, state, memory := valueOptionsFixture()
	options.MaximumNodes = 65536
	paths := make([]string, 16384)
	for i := range paths {
		paths[i] = fmt.Sprintf("internal/package/%05d.go", i)
	}
	value := map[string]any{"operands": paths, "paths": paths}
	projected, _, release, err := projector.Value(t.Context(), Scope{ProjectID: "fixture-project", SessionID: "fixture-session"}, "arguments", value, options)
	if err != nil {
		t.Fatalf("screen wide projection: %v", err)
	}
	if len(projected.(map[string]any)["paths"].([]string)) != len(paths) {
		t.Fatal("projection lost paths")
	}
	if state.peak > 32<<20 {
		t.Fatalf("projection uses %d state bytes", state.peak)
	}
	release()
	if state.outstanding != 0 || memory.outstanding != 0 {
		t.Fatal("projection retained leases")
	}
}
