package toolhost

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestDefaultRegistryKeepsOperationsOutsideTheToolRoster(t *testing.T) {
	config, err := nativemanifest.Load()
	testutil.FailErr(t, "load native manifest", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(extpackstest.StockCatalog(t))
	testutil.FailErr(t, "load stock metadata", err)
	registry, _, err := buildNativeRegistry(buildDeps{nativeConfig: config, toolSchemas: schemas, git: git.NewManager()})
	testutil.FailErr(t, "build default registry", err)
	operations, err := toolcontract.CommandOperations()
	testutil.FailErr(t, "load command operation identities", err)
	for _, name := range operations {
		if _, found := registry.Definition(name); found {
			t.Errorf("command-only operation %s is callable", name)
		}
		if _, found := registry.Operation(name); !found {
			t.Errorf("command-only operation %s has no binding", name)
		}
	}
	for _, name := range []string{"survey_repo", "summarize", "edit", "replace_lines", "code_rewrite", "jq_edit", "restore_version", "git_commit", "git_restore"} {
		if definition, found := registry.Definition(name); !found || definition.Handler == nil {
			t.Errorf("domain tool %s is unavailable", name)
		}
	}
}

func TestCommandEntryBindsRetiredOperationsWithoutCallableTools(t *testing.T) {
	fixture := newComposedProgramFixture(t)
	for _, name := range []string{"read", "write"} {
		if _, found := fixture.Runtime.Registry.Definition(name); found {
			t.Fatalf("operation %s has a callable definition", name)
		}
	}
	outcome := callComposedProgram(t, fixture, "printf 'alpha\\nbeta\\n' > sample.txt; cat sample.txt | wc -l")
	if outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "2\n" || outcome.CompletedEffects == 0 {
		t.Fatalf("native operation binding failed: %+v", outcome)
	}
}
