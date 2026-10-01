package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseCatalogToolIDs(t *testing.T) {
	ids, err := ParseCatalogToolIDs()
	testutil.FailErr(t, "ParseCatalogToolIDs failed", err)
	if len(ids) == 0 {
		t.Fatal("expected catalog ids")
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	for _, want := range []string{"parse_validate", "delegate_init", "state_query", "plan_append_review_evidence"} {
		if !found[want] {
			t.Fatalf("missing catalog id %q", want)
		}
	}
}

func TestValidateCatalogAllowlistSync(t *testing.T) {
	ids, err := ParseCatalogToolIDs()
	testutil.FailErr(t, "ParseCatalogToolIDs failed", err)
	registered := make(map[string]bool, len(ids))
	for _, id := range ids {
		registered[id] = true
	}
	if err := ValidateCatalogAllowlistSync(registered); err != nil {
		testutil.FailErr(t, "ValidateCatalogAllowlistSync failed", err)
	}

	delete(registered, ids[0])
	err = ValidateCatalogAllowlistSync(registered)
	if err == nil {
		t.Fatal("expected an unregistered allowlist id to fail")
	}
	if !strings.Contains(err.Error(), ids[0]) {
		t.Fatalf("error must name the missing id, got %q", err)
	}
}

func TestValidateBootToolClaimsHonest(t *testing.T) {
	reg := NewDefaultRegistry()
	for _, name := range bootToolClaimNames {
		if err := reg.Register(name, func(_ context.Context, _ map[string]any, _ ToolContext) (string, error) {
			return "ok", nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateBootToolClaimsHonest(reg); err != nil {
		testutil.FailErr(t, "ValidateBootToolClaimsHonest failed", err)
	}
	reg2 := NewDefaultRegistry()
	if err := ValidateBootToolClaimsHonest(reg2); err == nil {
		t.Fatal("expected missing claims error")
	}
}
