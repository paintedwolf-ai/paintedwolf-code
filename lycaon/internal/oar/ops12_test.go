package oar

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoaderRejectsUnresolvableDetectorRef(t *testing.T) {
	// [OAR-OPS-10]: unknown detector:// refs fail at load, not at dispatch.
	ensureCatalog(t)
	p, err := LoadEngineDeclaration(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load profile", err)
	prev := InstalledEngineDeclaration()
	InstallEngineDeclaration(p)
	t.Cleanup(func() { InstallEngineDeclaration(prev) })

	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	l.SetDetectors(NewDetectorRegistry())

	doc := map[string]any{
		"spec_version":   SupportedSpecVersion,
		"id":             "UNKNOWN_DETECTOR",
		"kind":           "detector",
		"on":             AnchorToolPreInvoke,
		"selector":       map[string]any{},
		"effect":         "block",
		"requires_facts": []any{"prompt_injection_score"},
		"when":           "prompt_injection_score > 0.5",
		"detector": map[string]any{
			"ref": "detector://not-registered",
		},
		"emit": "guard:detector",
	}
	_, skip, err := l.parseRule("UNKNOWN_DETECTOR", doc)
	if skip {
		t.Fatal("detector rule must not be skipped as ineligible")
	}
	if err == nil {
		t.Fatal("expected load rejection for unresolvable detector")
	}
	if !strings.Contains(err.Error(), "OAR-OPS-10") || !strings.Contains(err.Error(), "detector://not-registered") {
		t.Fatalf("error = %q, want [OAR-OPS-10] naming the ref", err)
	}
}
