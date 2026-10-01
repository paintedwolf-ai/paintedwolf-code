package session

import (
	"reflect"
	"testing"
)

func TestDiffWorkspaceSnapshots(t *testing.T) {
	baseline := map[string]WorkspaceFileFingerprint{
		"same.go":    {Size: 1, MtimeNano: 1},
		"changed.go": {Size: 1, MtimeNano: 1},
		"deleted.go": {Size: 1, MtimeNano: 1},
		"missing.go": {Size: 1, MtimeNano: 1},
	}
	current := map[string]WorkspaceFileFingerprint{
		"same.go":    {Size: 1, MtimeNano: 1},
		"changed.go": {Size: 2, MtimeNano: 2},
		"new.go":     {Size: 1, MtimeNano: 1},
	}
	want := []string{"changed.go", "deleted.go", "missing.go", "new.go"}
	if got := diffWorkspaceSnapshots(baseline, current); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %v, want %v", got, want)
	}
}
