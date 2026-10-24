package exec

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestAgentEnvironmentRoundTripsThroughProgramChildren(t *testing.T) {
	opts, err := AgentLaunchOptions(HostLaunch("fixture"), "", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := ProcessEnvironment(opts)
	if err != nil {
		t.Fatal(err)
	}
	child, err := AgentLaunchOptions(HostLaunch("fixture"), "", parent, nil, false)
	if err != nil {
		t.Fatalf("native environment cannot launch its child: %v", err)
	}
	childEnv, err := ProcessEnvironment(child)
	if err != nil {
		t.Fatal(err)
	}
	values := func(entries []string) map[string]string {
		out := map[string]string{}
		for _, entry := range entries {
			key, value, _ := strings.Cut(entry, "=")
			if _, duplicate := out[key]; duplicate {
				t.Fatalf("duplicate environment key %s", key)
			}
			out[key] = value
		}
		return out
	}
	if !maps.Equal(values(parent), values(childEnv)) {
		t.Fatal("native and child environments differ")
	}
	if _, err := AgentLaunchOptions(HostLaunch("fixture"), "", []string{"GIT_EDITOR=an-editor"}, nil, false); !errors.Is(err, ErrBlockedEnvKey) {
		t.Fatalf("reserved editor override accepted: %v", err)
	}
}
