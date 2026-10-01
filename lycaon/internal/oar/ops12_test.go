package oar

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoaderRejectsUnresolvableDetectorRef(t *testing.T) {
	// [OAR-OPS-12]: unknown detector:// refs fail at load, not at dispatch.
	ensureCatalog(t)
	p, err := LoadCapabilityDocument(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load profile", err)
	prev := InstalledCapabilityDocument()
	InstallCapabilityDocument(p)
	t.Cleanup(func() { InstallCapabilityDocument(prev) })

	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	l.SetDetectors(NewDetectorRegistry())

	doc := map[string]any{
		"oar":    SupportedSpecVersion,
		"id":     "UNKNOWN_DETECTOR",
		"kind":   "detector",
		"anchor": AnchorToolPreInvoke,
		"effect": "block",
		"when":   "prompt_injection_score > 0.5",
		"detector": map[string]any{
			"ref": "detector://not-registered",
		},
		"x-paintedwolf-emit": "guard:detector",
	}
	_, skip, err := l.parseRule("UNKNOWN_DETECTOR", doc)
	if skip {
		t.Fatal("detector rule must not be skipped as ineligible")
	}
	if err == nil {
		t.Fatal("expected load rejection for unresolvable detector")
	}
	if !strings.Contains(err.Error(), "OAR-OPS-12") || !strings.Contains(err.Error(), "detector://not-registered") {
		t.Fatalf("error = %q, want [OAR-OPS-12] naming the ref", err)
	}
}
