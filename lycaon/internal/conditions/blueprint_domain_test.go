package conditions

import "testing"

func TestBlueprintMaterializedText(t *testing.T) {
	if BlueprintMaterializedText("---\nstatus: draft\ntitle: placeholder\n---\n") {
		t.Fatal("empty draft marker must not be approval-ready")
	}
	if !BlueprintMaterializedText("---\ntitle: decision\n---\nKeep the package API.\n") {
		t.Fatal("authored body must be approval-ready")
	}
	if !BlueprintMaterializedText("---\nstatus: draft\ntitle: decision\n---\nKeep the package API.\n") {
		t.Fatal("authored draft content must be review-ready; approval is canonical host state")
	}
	if BlueprintMaterializedText("---\nstatus: approved\ntitle: decision\n---\nKeep the package API.\n") {
		t.Fatal("model-authored approval claim must not be approval-ready")
	}
}

func TestOptionsSelectionValidText(t *testing.T) {
	valid := "---\ncriterion: minimal surface\nwinner: package function\n---\nThe package API keeps direct unit tests and avoids CLI plumbing.\n"
	if !OptionsSelectionValidText(valid) {
		t.Fatal("complete Options selection must be valid")
	}
	for _, invalid := range []string{
		"---\nstatus: draft\ntitle: old request\n---\n",
		"---\nstatus: approved\ncriterion: minimal surface\nwinner: package function\n---\nrationale\n",
		"---\nwinner: package function\n---\nrationale\n",
		"---\ncriterion: minimal surface\n---\nrationale\n",
	} {
		if OptionsSelectionValidText(invalid) {
			t.Fatalf("incomplete Options selection accepted: %q", invalid)
		}
	}
}
