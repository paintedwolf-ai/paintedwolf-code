package catalogload

import (
	"os"
	"strings"

	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/session/profiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// manifests returns every bundled manifest and every resolved template.
func manifests(r *invariants.Run) map[string]workflowdef.Manifest {
	return invariants.Part(r.Corpus, "workflow manifests", workflowfixture.AllResolvedManifests)
}

// configPathExists reports a "config/…" catalog path present on disk.
func configPathExists(r *invariants.Run, rel string) bool {
	_, err := os.Stat(r.Path("lycaon/config/" + strings.TrimPrefix(strings.TrimSpace(rel), "config/")))
	return err == nil
}

var workflows = []invariants.Invariant{{
	// A manifest's rules files exist.
	Name:    "workflow-manifest-rules-paths-exist",
	Anchors: []invariants.Anchor{invariants.Func("test/contract/internal/workflowfixture", "AllResolvedManifests")},
	Check: func(r *invariants.Run) {
		all := manifests(r)
		r.Expect(len(all), "workflow manifests")
		for key, m := range all {
			for _, rel := range m.Rules {
				if strings.TrimSpace(rel) != "" && !configPathExists(r, rel) {
					r.Report("%s: rules path %s does not exist", key, rel)
				}
			}
		}
	},
}, {
	// A posture's rules files exist.
	Name:    "posture-registry-rules-paths-exist",
	Anchors: []invariants.Anchor{invariants.Func("internal/session/profiles", "LoadPostureRegistry")},
	Check: func(r *invariants.Run) {
		reg, err := profiles.LoadPostureRegistry()
		r.Must(err)
		postures := sessionposture.AllSessionPostures()
		r.Expect(len(postures), "session postures")
		for _, posture := range postures {
			spec, err := reg.Get(posture)
			r.Must(err)
			for _, rel := range spec.Rules {
				if !configPathExists(r, rel) {
					r.Report("posture %s: rules path %s does not exist", posture, rel)
				}
			}
		}
	},
}}
