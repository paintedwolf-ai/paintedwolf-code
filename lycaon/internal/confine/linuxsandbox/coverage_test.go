package linuxsandbox

import (
	"slices"
	"testing"
)

func TestCoverageEnvironmentCarriesOnlyTheCoverageDirectory(t *testing.T) {
	t.Setenv("GOCOVERDIR", "")
	if env := coverageEnvironment(); len(env) != 0 {
		t.Fatalf("without a coverage directory the environment must be empty, got %q", env)
	}
	t.Setenv("GOCOVERDIR", "/run/coverage")
	if env := coverageEnvironment(); !slices.Equal(env, []string{"GOCOVERDIR=/run/coverage"}) {
		t.Fatalf("the environment must name the coverage directory and nothing else, got %q", env)
	}
}
