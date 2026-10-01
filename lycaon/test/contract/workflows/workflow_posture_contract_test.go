package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestBundledWorkflowManifestPosturesValid(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for key, m := range catalog {
		checkManifestPostures(t, key, m)
	}
}

func checkManifestPostures(t *testing.T, key string, m workflowdef.Manifest) {
	t.Helper()
	if ip := strings.TrimSpace(m.InitialPosture); ip != "" {
		assertValidPosture(t, key, "initial_posture", ip)
		assertNotPhaseLikePosture(t, key, "initial_posture", ip, m)
	}
	for _, def := range m.PhaseDefs {
		if sp := strings.TrimSpace(def.OnEnter.SetPosture); sp != "" {
			assertValidPosture(t, key, "on_enter.set_posture on phase "+def.ID, sp)
			assertNotPhaseLikePosture(t, key, "set_posture", sp, m)
		}
	}
}

func assertValidPosture(t *testing.T, manifestKey, field, value string) {
	t.Helper()
	if !session.ValidSessionPosture(value) {
		t.Errorf("%s %s = %q is not a valid SessionPosture", manifestKey, field, value)
	}
	for _, legacy := range []string{"plan", "implement", "delegation", "security"} {
		if value == legacy {
			t.Errorf("%s %s uses legacy posture value %q", manifestKey, field, value)
		}
	}
}

func assertNotPhaseLikePosture(t *testing.T, manifestKey, field, posture string, m workflowdef.Manifest) {
	t.Helper()
	for _, phaseID := range m.Phases {
		if posture == phaseID {
			t.Errorf("%s %s = %q conflates posture with phase id", manifestKey, field, posture)
		}
	}
	for _, def := range m.PhaseDefs {
		if posture == def.ID {
			t.Errorf("%s %s = %q conflates posture with phase id %q", manifestKey, field, posture, def.ID)
		}
	}
}

func TestParseManifestRejectsInvalidSetPosture(t *testing.T) {
	t.Parallel()
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: research
    on_enter:
      set_posture: research
`))
	if err == nil {
		t.Fatal("expected parse failure for phase id as set_posture")
	}
}
