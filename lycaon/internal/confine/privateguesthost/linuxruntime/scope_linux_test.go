package linuxruntime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResourceSelection(t *testing.T) {
	for _, line := range []string{"", "0::relative", "0::/../b", "0:://b", "1:cpu:/x"} {
		if _, err := unifiedGroup(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
	got, err := unifiedGroup("2:cpu:/legacy\n0::/session\n")
	if err != nil || got != "/sys/fs/cgroup/session" {
		t.Fatalf("unified domain %q, %v", got, err)
	}
	if !contained(got, got+"/child") || contained(got, got+"-other") {
		t.Fatal("unexpected cgroup containment")
	}
	origin := t.TempDir()
	leaf, err := delegatedLeaf(origin)
	if err != nil || leaf != "" {
		t.Fatalf("absent delegate %q %v", leaf, err)
	}
	control := filepath.Join(origin, "cgroup.subtree_control")
	if err := os.WriteFile(control, []byte("memory"), 0600); err != nil {
		t.Fatalf("write controller fixture: %v", err)
	}
	if leaf, err := delegatedLeaf(origin); err != nil || leaf != "" {
		t.Fatalf("partial delegate %q %v", leaf, err)
	}
	if err := os.WriteFile(control, []byte("memory pids"), 0600); err != nil {
		t.Fatalf("write full controller fixture: %v", err)
	}
	leaf, err = delegatedLeaf(origin)
	if err != nil || filepath.Dir(leaf) != origin {
		t.Fatalf("workspace resource leaf %q %v", leaf, err)
	}
}

func TestControllerStartsFromSystemRoot(t *testing.T) {
	cmd := ControllerCommand(t.Context(), "/fixture/controller", "--launch-native")
	if cmd.Dir != "/" || cmd.Env == nil || len(cmd.Env) != 0 || cmd.Stderr != os.Stderr || !slices.Equal(cmd.Args, []string{"/fixture/controller", "--launch-native"}) {
		t.Fatalf("controller path=%q dir=%q env=%v args=%q", cmd.Path, cmd.Dir, cmd.Env, cmd.Args)
	}
}
