package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestEveryNativeGitOperationProducesCitableOutcomeEvidence(t *testing.T) {
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	for tool, owner := range manifest.Owner {
		if owner != "git" {
			continue
		}
		kind := binding.ToolKind(tool)
		if kind == "" || binding.KindShape(kind) != evidence.ShapeCommand {
			t.Fatalf("%s has no citable Git outcome receipt", tool)
		}
		if manifest.Lifecycle[tool] == nativemanifest.LifecycleEffectAttempt && binding.IsSurveyKind(kind) {
			t.Fatalf("%s effect is represented only as a survey", tool)
		}
	}
}
