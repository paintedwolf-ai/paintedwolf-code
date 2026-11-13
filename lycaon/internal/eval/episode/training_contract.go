package episode

import (
	"runtime/debug"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/trainingcapture"
)

const (
	ContractSchema = "pw-training-contract/1"
	BundleSchema   = "pw-training-episode-bundle/1"
)

// BundleFiles are the files every episode bundle holds beside manifest.json
// and its blobs directory.
var BundleFiles = []string{
	"catalog.json", "sessions.jsonl", "captures.jsonl", "operations.jsonl", "observations.jsonl",
	"effects.jsonl", "decisions.jsonl", "completions.jsonl", "source-inventory.json",
}

// TrainingContract is what a factory needs to read this host's evidence
// without naming its tools or its storage: the record schemas, the bundle
// layout, the registered operation sources and the closed vocabularies.
type TrainingContract struct {
	Schema          string            `json:"schema"`
	HostCommit      string            `json:"host_commit"`
	HostModified    bool              `json:"host_modified"`
	Bundle          string            `json:"bundle"`
	Capture         string            `json:"capture"`
	Fact            string            `json:"fact"`
	Files           []string          `json:"files"`
	Sources         []SourceIdentity  `json:"sources"`
	Settlement      []Settlement      `json:"settlement"`
	SemanticOutcome []SemanticOutcome `json:"semantic_outcome"`
	EffectState     []EffectState     `json:"effect_state"`
	OfferSources    []string          `json:"offer_sources"`
}

// Contract describes this build.
func Contract() TrainingContract {
	commit, modified := buildRevision()
	return TrainingContract{
		Schema: ContractSchema, HostCommit: commit, HostModified: modified,
		Bundle: BundleSchema, Capture: trainingcapture.Schema, Fact: trainingcapture.FactSchema,
		Files:           append([]string(nil), BundleFiles...),
		Sources:         SourceIdentities(),
		Settlement:      []Settlement{Settled, Unsettled},
		SemanticOutcome: []SemanticOutcome{OutcomeSuccess, OutcomeFailure, OutcomeUnknown},
		EffectState:     []EffectState{EffectsNone, EffectsKnown, EffectsUnknown},
		OfferSources: []string{"standing", string(turnload.SourcePredicted), string(turnload.SourceRequested),
			string(turnload.SourceFallback), string(turnload.SourceCompanion)},
	}
}

func buildRevision() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	var commit string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			commit = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return commit, modified
}
