package project

import (
	"errors"
	"testing"
)

func TestResolveWorkspaceRootRequiresPrimary(t *testing.T) {
	p := &Project{Roots: []Root{{ID: "root", Path: t.TempDir()}}}
	if _, _, err := ResolveWorkspaceRoot(p, ""); !errors.Is(err, ErrRootNotFound) {
		t.Fatalf("err = %v, want ErrRootNotFound", err)
	}
}
