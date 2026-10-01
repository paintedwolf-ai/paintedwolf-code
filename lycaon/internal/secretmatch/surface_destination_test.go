package secretmatch

import "testing"

// Every screen surface names who receives a send. The card states this rather
// than leaving a person to infer it from a destination label.
func TestEverySurfaceNamesItsReceiver(t *testing.T) {
	t.Parallel()
	want := map[ScreenSurface]DestinationKind{
		SurfaceWebSearch:   DestinationService,
		SurfaceFetchURL:    DestinationService,
		SurfaceHTTPRequest: DestinationService,
		SurfaceMCP:         DestinationService,
		SurfaceModel:       DestinationModelProvider,
		SurfaceVisualModel: DestinationModelProvider,
		SurfaceCommand:     DestinationProcess,
		SurfaceTerminal:    DestinationProcess,
		SurfaceFile:        DestinationFile,
	}
	for surface := range surfaceFacts {
		got := surface.DestinationKind()
		if got == "" {
			t.Errorf("%s has no destination kind", surface)
		}
		if expected, ok := want[surface]; ok && got != expected {
			t.Errorf("%s receiver = %q, want %q", surface, got, expected)
		}
	}
	if len(want) != len(surfaceFacts) {
		t.Fatalf("surface table has %d entries, the receiver map has %d", len(surfaceFacts), len(want))
	}
}

// Redaction fails a call whose credential authenticates it. A redacted model
// request still completes, so redaction is not a failure on that seam.
func TestRedactionBreaksOnlyWhereTheValueAuthenticates(t *testing.T) {
	t.Parallel()
	for _, surface := range []ScreenSurface{SurfaceHTTPRequest, SurfaceMCP, SurfaceWebSearch, SurfaceFetchURL} {
		if !surface.RedactionBreaksRequest() {
			t.Errorf("%s should report that redaction breaks the call", surface)
		}
	}
	if SurfaceModel.RedactionBreaksRequest() {
		t.Error("a model request still completes without the value")
	}
	// An argv cannot be rewritten at all, so the question never arises.
	for _, surface := range []ScreenSurface{SurfaceCommand, SurfaceTerminal} {
		if surface.CanRedact() {
			t.Errorf("%s should not offer redaction", surface)
		}
	}
}

// Managed evidence is exact, not a shape guess.
func TestManagedEvidenceIsRuleIdentity(t *testing.T) {
	t.Parallel()
	if !IsManagedRule(ManagedRuleID) {
		t.Fatal("the managed rule id must report managed evidence")
	}
	if IsManagedRule("gitleaks:aws-access-token") {
		t.Fatal("a shape rule is not managed evidence")
	}
	if !(Alert{RuleID: ManagedRuleID}).Managed() {
		t.Fatal("alert did not carry its managed evidence")
	}
}
