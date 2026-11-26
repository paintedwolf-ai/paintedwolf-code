package definition

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lycaon/lycaon/internal/testutil"
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
	original := Manifest{
		ID:      "clone-audit",
		Version: "1.0.0",
		Phases:  []string{"plan"},
		PhaseDefs: []PhaseDef{
			{
				ID: "plan",
				Explain: &PhaseExplain{
					Summary: "Original summary",
					Body:    "Original body",
				},
				OnEnter: PhaseOnEnter{
					RequestUserFeedback: &UserFeedbackPrompt{
						Prompt:      "Approve?",
						Options:     []string{"yes", "no"},
						ArtifactIDs: []string{"art-1"},
						Secret: &SecretInputSpec{
							Name:               "API_TOKEN",
							Purpose:            "auth",
							Scope:              "session",
							AgentUseTTLSeconds: 60,
						},
					},
					Obligations: []ObligationDef{
						{
							Kind: "check",
							Params: map[string]any{
								"nested": map[string]any{"key": "orig"},
								"items":  []any{"a", "b"},
							},
						},
					},
				},
				ContentReview: &PhaseContentReview{
					Tools: []string{"edit"},
					Paths: []string{"src/"},
				},
			},
		},
	}

	raw, err := EncodeRunManifest(original)
	testutil.FailErr(t, "encode run manifest", err)

	first, err := DecodeRunManifest(raw)
	testutil.FailErr(t, "first decode", err)

	// Mutate every nested mutable field on the first decoded manifest.
	first.Phases[0] = "mutated-phase"
	first.PhaseDefs[0].Explain.Summary = "Mutated summary"
	first.PhaseDefs[0].Explain.Body = "Mutated body"
	first.PhaseDefs[0].OnEnter.RequestUserFeedback.Options[0] = "mutated-opt"
	first.PhaseDefs[0].OnEnter.RequestUserFeedback.ArtifactIDs[0] = "mutated-art"
	first.PhaseDefs[0].OnEnter.RequestUserFeedback.Secret.Name = "MUTATED_SECRET"
	first.PhaseDefs[0].OnEnter.Obligations[0].Params["nested"].(map[string]any)["key"] = "mutated"
	first.PhaseDefs[0].OnEnter.Obligations[0].Params["items"].([]any)[0] = "mutated-item"
	first.PhaseDefs[0].ContentReview.Tools[0] = "mutated-tool"

	second, err := DecodeRunManifest(raw)
	testutil.FailErr(t, "second decode", err)

	if diff := cmp.Diff(original, second, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("cache leaked caller mutation on re-decode:\n%s", diff)
	}
}
