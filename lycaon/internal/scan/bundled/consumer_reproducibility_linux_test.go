//go:build linux

package bundled_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConsumerIdentityBindsProductResultsWithoutHostDiagnostics(t *testing.T) {
	producer, initialRoot, recipe := consumerFixture(t)
	finalBytes, err := os.ReadFile(bundled.ArtifactPath(initialRoot, producer.OpenGrep.Version, "linux"))
	testutil.FailErr(t, "retain identical transformed fixture", err)
	var identities []string
	for index := range 3 {
		root := t.TempDir()
		path, err := bundled.StageArtifact(t.Context(), producer, root, "linux", runtime.GOARCH)
		testutil.FailErr(t, "stage independent consumer fixture", err)
		testutil.FailErr(t, "use identical final bytes", os.WriteFile(path, finalBytes, 0o755))
		selected, err := bundled.FinalizeConsumerArtifact(t.Context(), producer, root, recipe, func(_ context.Context, prepared *bundled.Manifest) (json.RawMessage, error) {
			report := consumerReport(t, prepared)
			report["engine_root"], report["binary"] = root, path
			report["linux_capabilities"] = map[string]any{"landlock_abi": 4 + index, "apparmor_label": root}
			if index == 2 {
				report["cases"].([]any)[0].(map[string]any)["expected_rule"] = "other-rule"
			}
			return buildJSON(t, report), nil
		})
		testutil.FailErr(t, "qualify deterministic consumer fixture", err)
		encoded, err := bundled.EncodedBuildIdentity(selected)
		testutil.FailErr(t, "encode finalized authority", err)
		identities = append(identities, encoded)
		record, err := os.ReadFile(filepath.Join(bundled.EngineBundledDir(root, producer.OpenGrep.Version), "consumer-finalization.json"))
		testutil.FailErr(t, "inspect deterministic record", err)
		if strings.Contains(string(record), root) || strings.Contains(string(record), "linux_capabilities") {
			t.Fatal("host diagnostics entered the deterministic consumer record")
		}
	}
	if identities[0] != identities[1] {
		t.Fatal("host paths or kernel diagnostics changed the finalized build identity")
	}
	if identities[0] == identities[2] {
		t.Fatal("changed product case results retained the same finalized identity")
	}
}
