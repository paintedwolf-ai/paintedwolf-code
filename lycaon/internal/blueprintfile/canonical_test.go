package blueprintfile_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func TestBoundBlueprintRelPath(t *testing.T) {
	got := blueprintfile.BoundBlueprintRelPath(settingsoverlay.Rel("blueprints/weather.md"))
	if got != settingsoverlay.DirName()+"/blueprints/weather.md" {
		t.Fatalf("bound path: got %q", got)
	}
	got = blueprintfile.BoundBlueprintRelPath("")
	if got != "" {
		t.Fatalf("empty unbound: got %q", got)
	}
	got = blueprintfile.BoundBlueprintRelPath("plans/weather.md")
	if got != "" {
		t.Fatalf("non-convention path: got %q", got)
	}
}
