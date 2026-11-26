package definition

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestRunSnapshotRestoresEveryBundledDefinition(t *testing.T) {
	reg, err := RegistryFromDirs("")
	testutil.FailErr(t, "load catalog", err)
	for key, m := range reg.All() {
		t.Run(key, func(t *testing.T) {
			data, err := EncodeRunManifest(m)
			testutil.FailErr(t, "capture admitted workflow", err)
			got, err := DecodeRunManifest(data)
			testutil.FailErr(t, "restore run definition", err)
			if diff := cmp.Diff(m, got, cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("snapshot changed resolved workflow:\n%s", diff)
			}
		})
	}
}

func TestRunSnapshotRejectsUnknownShapes(t *testing.T) {
	for _, raw := range []string{"manifest: [", "snapshot_version: 2", "snapshot_version: 1", "snapshot_version: 1\nfuture: true"} {
		if _, err := DecodeRunManifest(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestDecodeRunManifestCacheReturnsDetachedClone(t *testing.T) {
	raw := `snapshot_version: 1
manifest:
  id: clone-audit
  version: 1.0.0
  phases:
    - plan
  phasedefs:
    - id: plan
      explain:
        summary: Original summary
        body: Original body
      onenter:
        requestuserfeedback:
          prompt: Approve?
          options:
            - yes
            - no
          artifactids:
            - art-1
          secret:
            name: API_TOKEN
            purpose: auth
            scope: session
            agentusettlseconds: 60
        obligations:
          - kind: check
            params:
              nested:
                key: orig
              items:
                - a
                - b
              numeric:
                1: orig
                2: two
          - kind: empty-params
            params: {}
          - kind: nil-params
      contentreview:
        tools:
          - edit
        paths:
          - src/
`

	var direct runManifestV1
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	testutil.FailErr(t, "direct yaml decode", dec.Decode(&direct))

	first, err := DecodeRunManifest(raw)
	testutil.FailErr(t, "first decode", err)
	if diff := cmp.Diff(direct.Manifest, first); diff != "" {
		t.Fatalf("first cached decode diverged from direct YAML decode:\n%s", diff)
	}
	if first.PhaseDefs[0].OnEnter.Obligations[1].Params == nil {
		t.Fatal("explicitly empty params: {} decoded as nil map")
	}
	if first.PhaseDefs[0].OnEnter.Obligations[2].Params != nil {
		t.Fatalf("omitted params decoded as non-nil map: %#v", first.PhaseDefs[0].OnEnter.Obligations[2].Params)
	}

	// Mutate every nested mutable field on the first decoded manifest, including
	// inserting into the explicitly empty params map and mutating the numeric-key map.
	first.Phases[0] = "mutated-phase"
	first.PhaseDefs[0].Explain.Summary = "Mutated summary"
	first.PhaseDefs[0].Explain.Body = "Mutated body"
	first.PhaseDefs[0].OnEnter.RequestUserFeedback.Options[0] = "mutated-opt"
	first.PhaseDefs[0].OnEnter.RequestUserFeedback.ArtifactIDs[0] = "mutated-art"
	first.PhaseDefs[0].OnEnter.RequestUserFeedback.Secret.Name = "MUTATED_SECRET"
	first.PhaseDefs[0].OnEnter.Obligations[0].Params["nested"].(map[string]any)["key"] = "mutated"
	first.PhaseDefs[0].OnEnter.Obligations[0].Params["items"].([]any)[0] = "mutated-item"
	numericMap, ok := first.PhaseDefs[0].OnEnter.Obligations[0].Params["numeric"].(map[any]any)
	if !ok {
		t.Fatalf("expected map[any]any for numeric-key YAML mapping, got %T", first.PhaseDefs[0].OnEnter.Obligations[0].Params["numeric"])
	}
	numericMap[1] = "mutated"
	first.PhaseDefs[0].OnEnter.Obligations[1].Params["added"] = "should-not-leak"
	first.PhaseDefs[0].ContentReview.Tools[0] = "mutated-tool"

	second, err := DecodeRunManifest(raw)
	testutil.FailErr(t, "second decode", err)

	if diff := cmp.Diff(direct.Manifest, second); diff != "" {
		t.Fatalf("cache leaked caller mutation or diverged from direct YAML decode on re-decode:\n%s", diff)
	}
	if second.PhaseDefs[0].OnEnter.Obligations[1].Params == nil || len(second.PhaseDefs[0].OnEnter.Obligations[1].Params) != 0 {
		t.Fatalf("second decode empty params corrupted: %#v", second.PhaseDefs[0].OnEnter.Obligations[1].Params)
	}
	if got := second.PhaseDefs[0].OnEnter.Obligations[0].Params["numeric"].(map[any]any)[1]; got != "orig" {
		t.Fatalf("second decode numeric map[1] = %v, want orig", got)
	}
}
