package invariants_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestStructuralFragmentsTransitiveInvariantFailsClosed(t *testing.T) {
	t.Parallel()
	inv := rule(t, promptrender.Family, "structural-fragments-stay-capability-and-action-free")
	anchors := map[string]string{
		"lycaon/internal/prompts/guidance_renderer.go": "package prompts\n\nfunc StructuralGuidanceFragments() []string { return nil }\n",
		"lycaon/internal/prompts/inject_renderer.go":   "package prompts\n\nfunc StructuralInjectFragments() []string { return nil }\n",
	}
	cleanCorpus := with(anchors, map[string]string{
		"lycaon/config/packs/painted-wolf/platform/guidance/survey/jq-zoom.md":   "Matched {{ count }} nodes across {{ file_count }} files.\n{% include \"partials/zoom-note.md\" %}\n",
		"lycaon/config/packs/painted-wolf/platform/guidance/view-context.md":     "Active editor: {{ active_file }}.\n",
		"lycaon/config/packs/painted-wolf/platform/shared/partials/zoom-note.md": "Narrow the selector path before reading full objects.\n",
	})

	for name, tc := range map[string]struct {
		files   map[string]string
		want    outcome
		subtext string
	}{
		"clean structural fragment and partial": {
			files: cleanCorpus,
			want:  passes,
		},
		"seeded direct actionable command in structural fragment": {
			files: with(cleanCorpus, map[string]string{
				"lycaon/config/packs/painted-wolf/platform/guidance/survey/jq-zoom.md": "Matched {{ count }} nodes (sed -n '1,20p' {{ output_path }}).\n",
			}),
			want:    violates,
			subtext: `claims callable tool or command "sed"`,
		},
		"seeded direct invented capability boolean in structural fragment": {
			files: with(cleanCorpus, map[string]string{
				"lycaon/config/packs/painted-wolf/platform/guidance/survey/jq-zoom.md": "{% if command_available %}Matched {{ count }} nodes.{% endif %}\n",
			}),
			want:    violates,
			subtext: `references capability fact "command_available"`,
		},
		"seeded transitive actionable fragment in included partial": {
			files: with(cleanCorpus, map[string]string{
				"lycaon/config/packs/painted-wolf/platform/shared/partials/zoom-note.md": "Recover omitted bytes with `recall` before continuing.\n",
			}),
			want:    violates,
			subtext: "included from config/packs/painted-wolf/platform/guidance/survey/jq-zoom.md",
		},
		"seeded transitive capability boolean in included partial": {
			files: with(cleanCorpus, map[string]string{
				"lycaon/config/packs/painted-wolf/platform/shared/partials/zoom-note.md": "{% if recall_available %}Keep paging.{% endif %}\n",
			}),
			want:    violates,
			subtext: `references capability fact "recall_available"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(fixture(t, tc.files), inv)
			expect(t, res, tc.want, 0)
			if tc.subtext != "" && !slices.ContainsFunc(res.Violations, func(v string) bool { return strings.Contains(v, tc.subtext) }) {
				t.Fatalf("expected violation containing %q, got:\n%s", tc.subtext, strings.Join(res.Violations, "\n"))
			}
		})
	}
}
