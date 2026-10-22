package trainingcapture

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUtilityHealthContainsOnlyProtocolFacts(t *testing.T) {
	root := t.TempDir()
	configure(t, root)
	for _, err := range []error{nil, errors.New("private diagnostic"), &failure.ProviderOutputTruncatedError{FinishReason: "length"}} {
		ObserveUtility(t.Context(), "compaction", err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	testutil.FailErr(t, "list health records", err)
	var success, truncated int
	for _, path := range paths {
		body, err := os.ReadFile(path)
		testutil.FailErr(t, "read health record", err)
		var health UtilityHealth
		testutil.FailErr(t, "decode health record", json.Unmarshal(body, &health))
		if health.Success {
			success++
		}
		if health.FinishReason == "length" {
			truncated++
		}
		if health.Schema != "pw-training-health/1" || health.Epoch != "fixture" || health.Purpose != "compaction" || health.DecidedAt == "" {
			t.Fatalf("health = %+v", health)
		}
	}
	if len(paths) != 3 || success != 1 || truncated != 1 {
		t.Fatalf("records=%d success=%d truncated=%d", len(paths), success, truncated)
	}
}
