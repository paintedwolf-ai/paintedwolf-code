package sandbox

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadPathScopes(t *testing.T) {
	reg, err := LoadPathScopes()
	testutil.FailErr(t, "LoadPathScopes failed", err)
	coord, ok := reg["coordinator_orchestration"]
	if !ok {
		t.Fatal("missing coordinator_orchestration scope")
	}
	if len(coord.Read) == 0 || len(coord.Write) == 0 {
		t.Fatalf("coordinator_orchestration scope incomplete: read=%v write=%v", coord.Read, coord.Write)
	}
	plan, ok := reg["plan_write"]
	if !ok {
		t.Fatal("missing plan_write scope")
	}
	if len(plan.Write) != 1 || plan.Write[0] != settingsoverlay.DirName()+"/blueprints/**" {
		t.Fatalf("plan_write write globs = %v", plan.Write)
	}
}

func TestCheckWriteInScopeCoordinatorProductWrite(t *testing.T) {
	reg, err := LoadPathScopes()
	testutil.FailErr(t, "LoadPathScopes", err)
	scope := reg["coordinator_product_write"]
	// Overlay paths are in scope; the approval gate reviews the ones a trust
	// surface loads.
	allowed := []string{
		"src/main.go",
		settingsoverlay.Rel("blueprints/note.md"),
		settingsoverlay.Rel("rules/x.yaml"),
		settingsoverlay.Rel("scratch.md"),
	}
	for _, path := range allowed {
		if err := CheckWriteInScope(scope, "coordinator_product_write", path); err != nil {
			t.Fatalf("expected %q allowed, got %v", path, err)
		}
	}
}

func TestCheckWriteInScopeDefaultDeny(t *testing.T) {
	emptyWrite := PathScope{}
	for _, path := range []string{"src/main.go", settingsoverlay.Rel("blueprints/x.md"), "any/where/file.go"} {
		if err := CheckWriteInScope(emptyWrite, "test_empty_write", path); err == nil {
			t.Fatalf("empty Write scope must deny %q", path)
		}
	}

	reg, err := LoadPathScopes()
	testutil.FailErr(t, "LoadPathScopes", err)
	readOnly := reg["coordinator_product_read"]
	for _, path := range []string{"src/main.go", "README.md"} {
		if err := CheckWriteInScope(readOnly, "coordinator_product_read", path); err == nil {
			t.Fatalf("read-only scope must deny write to %q", path)
		}
	}
}

func TestCheckWriteInScopePrecedence(t *testing.T) {
	scope := PathScope{
		Write: []string{"**"},
		Deny:  []string{settingsoverlay.Rel("rules/**")},
	}
	cases := []struct {
		path    string
		allowed bool
		reason  string
	}{
		{settingsoverlay.Rel("blueprints/x.md"), true, "Write ** reaches the overlay"},
		{settingsoverlay.Rel("rules/x.yaml"), false, "Deny beats Write"},
		{"src/main.go", true, "Write allowlist matches"},
		{"node_modules/x.js", true, "Write ** matches"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			err := CheckWriteInScope(scope, "precedence_test", tc.path)
			if tc.allowed && err != nil {
				t.Fatalf("expected allow: %v", err)
			}
			if !tc.allowed && err == nil {
				t.Fatalf("expected deny for %q", tc.path)
			}
		})
	}

	denyOverWrite := PathScope{
		Write: []string{"src/**"},
		Deny:  []string{"src/secret/**"},
	}
	if err := CheckWriteInScope(denyOverWrite, "deny_over_write", "src/secret/key.go"); err == nil {
		t.Fatal("Deny must beat Write when path matches both")
	}
	if err := CheckWriteInScope(denyOverWrite, "deny_over_write", "src/public.go"); err != nil {
		t.Fatalf("Write should allow non-denied path: %v", err)
	}
}

func TestPathScopeDenyRequiresExplicitWrite(t *testing.T) {
	reg, err := LoadPathScopes()
	testutil.FailErr(t, "LoadPathScopes", err)
	for id, scope := range reg {
		if len(scope.Deny) == 0 {
			continue
		}
		if len(scope.Write) == 0 {
			t.Fatalf("scope %q denies paths but has no write allowlist — use write: [\"**\"] or explicit globs", id)
		}
	}
}

func TestResolveScopeGlobsUnknown(t *testing.T) {
	_, _, err := ResolveScopeGlobs(PathScopeRegistry{}, "missing")
	if err == nil {
		t.Fatal("expected error for unknown scope")
	}
}
