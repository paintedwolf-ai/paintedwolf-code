package hostcmd

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	lyexec "github.com/lycaon/lycaon/internal/exec"
)

func TestExecutionKeyIsStableAcrossMapAndRootOrder(t *testing.T) {
	left := Request{
		ProjectDir: "/project", ProfileID: "implement",
		Stages:   []lyexec.Stage{{Name: "tool", Args: []string{"check"}}},
		IOParams: IOParams{InlineEnv: map[string]string{"B": "2", "A": "1"}},
		Launch:   lyexec.AgentLaunch(lyexec.LaunchAgentCommand, "test", &confine.Confinement{Roots: []string{"/b", "/a"}}),
	}
	right := left
	right.InlineEnv = map[string]string{"A": "1", "B": "2"}
	right.Launch.Confinement = &confine.Confinement{
		Roots:     []string{"/a", "/b"},
		ProxyAddr: "127.0.0.1:54321",
	}
	if ExecutionKey(left) != ExecutionKey(right) {
		t.Fatal("equivalent execution requests must have the same key")
	}
}

func TestExecutionKeyChangesForMaterialInput(t *testing.T) {
	base := Request{
		Launch:     lyexec.HostLaunch("test"),
		ProjectDir: "/project", ProfileID: "implement",
		Stages: []lyexec.Stage{{Name: "tool", Args: []string{"check"}}},
	}
	changed := base
	changed.Stages = []lyexec.Stage{{Name: "tool", Args: []string{"test"}}}
	if ExecutionKey(base) == ExecutionKey(changed) {
		t.Fatal("different argv must have different execution keys")
	}
}
