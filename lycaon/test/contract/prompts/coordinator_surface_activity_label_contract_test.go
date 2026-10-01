package contract

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Every selectable surface supplies its activity label.
func TestEveryCoordinatorSurfaceDeclaresAnActivityLabel(t *testing.T) {
	t.Parallel()
	surfaces, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	if len(surfaces) == 0 {
		t.Fatal("no coordinator surfaces loaded")
	}

	seen := map[string]string{}
	for id := range surfaces {
		label := surface.LoadCoordinatorSurfaceActivityLabel(id)
		if strings.TrimSpace(label) == "" {
			t.Errorf("surface %q has no activity_label — Den renders nothing for its turns", id)
			continue
		}
		if label != strings.TrimSpace(label) {
			t.Errorf("surface %q activity_label %q has surrounding whitespace", id, label)
		}
		// Den supplies the trailing marker.
		if strings.HasSuffix(label, ".") || strings.HasSuffix(label, "…") || strings.HasSuffix(label, "...") {
			t.Errorf("surface %q activity_label %q ends in its own punctuation", id, label)
		}
		if n := utf8.RuneCountInString(label); n > 48 {
			t.Errorf("surface %q activity_label is %d runes; the composer lane ellipsizes past ~48", id, n)
		}
		if first, _ := utf8.DecodeRuneInString(label); !unicode.IsUpper(first) {
			t.Errorf("surface %q activity_label %q does not start with a capital", id, label)
		}
		if prev, dup := seen[label]; dup {
			t.Errorf("surfaces %q and %q share activity_label %q — a copy-paste, or two states that need distinguishing", prev, id, label)
		}
		seen[label] = id
	}
}

// The no-folder row is not selectable.
func TestNoFolderAllowlistIsNotASurface(t *testing.T) {
	t.Parallel()
	if label := surface.LoadCoordinatorSurfaceActivityLabel("no_folder_allowlist"); label != "" {
		t.Fatalf("no_folder_allowlist has activity_label %q; it is an allow-list, not a surface", label)
	}
	surfaces, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	if _, ok := surfaces["no_folder_allowlist"]; ok {
		t.Fatal("no_folder_allowlist appears in the surface set")
	}
}

// Surface activity_label and the batch-phase chip share one label per phase.
func TestBatchPhaseActivityLabelTwinsCoordinatorSurfaces(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	vocab := contractcheck.ReadRepoFile(t, root, "docs/openapi/vocab/CoordinatorBatchPhase.yaml")
	var doc struct {
		Values []struct {
			ID    string `yaml:"id"`
			Label string `yaml:"label"`
		} `yaml:"values"`
	}
	if err := yaml.Unmarshal([]byte(vocab), &doc); err != nil {
		t.Fatalf("parse CoordinatorBatchPhase vocab: %v", err)
	}
	labels := map[string]string{}
	for _, v := range doc.Values {
		labels[v.ID] = v.Label
	}
	twins := map[string]string{
		"implement_dispatch":        "dispatch",
		"implement_overlay_promote": "integrate",
		"implement_synthesis":       "synthesize",
	}
	for surfaceID, phase := range twins {
		want := labels[phase]
		if want == "" {
			t.Fatalf("vocab missing label for batch phase %q", phase)
		}
		got := surface.LoadCoordinatorSurfaceActivityLabel(surfaceID)
		if got != want {
			t.Errorf("surface %q activity_label %q != batch phase %q label %q", surfaceID, got, phase, want)
		}
	}
}
