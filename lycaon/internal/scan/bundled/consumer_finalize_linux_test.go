//go:build linux

package bundled_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func consumerFixture(t *testing.T) (*bundled.Manifest, string, bundled.ConsumerRecipe) {
	t.Helper()
	f := releaseFixtureFromBuild(t, linuxBuildFixture(t, runtime.GOARCH))
	producer := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive, Offline: true}).Manifest
	root := t.TempDir()
	path, err := bundled.StageArtifact(t.Context(), producer, root, "linux", runtime.GOARCH)
	testutil.FailErr(t, "stage consumer fixture", err)
	program, err := exec.LookPath("true")
	testutil.FailErr(t, "find native ELF fixture", err)
	raw, err := os.ReadFile(program)
	testutil.FailErr(t, "read native ELF fixture", err)
	testutil.FailErr(t, "write final ELF fixture", os.WriteFile(path, raw, 0o755))
	tool := filepath.Join(t.TempDir(), "fixture-relocator")
	toolBytes := []byte("declared fixture relocation tool")
	testutil.FailErr(t, "write declared tool fixture", os.WriteFile(tool, toolBytes, 0o700))
	return producer, root, bundled.ConsumerRecipe{SchemaVersion: 1, Kind: "nix-elf-relocation", SourceCommit: strings.Repeat("a", 40),
		FlakeLockSHA256: strings.Repeat("b", 64), ToolPath: tool, ToolSHA256: buildDigest(toolBytes), Arguments: []string{"fixture"}}
}

func consumerReport(t *testing.T, manifest *bundled.Manifest) map[string]any {
	t.Helper()
	identity, err := manifest.ArtifactForCurrentPlatform()
	testutil.FailErr(t, "read prepared final authority", err)
	return map[string]any{"schema_version": 1, "status": "passed", "goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"binary_sha256": identity.SHA256, "binary_bytes": identity.BinaryBytes, "confined": true,
		"cases": []any{map[string]any{"file": "positive.rs", "expected_rule": "fixture", "findings": 1, "passed": true},
			map[string]any{"file": "negative.rs", "findings": 0, "passed": true}}}
}

func TestConsumerFinalizationPreservesProducerAndFinalAuthority(t *testing.T) {
	producer, root, recipe := consumerFixture(t)
	original, err := bundled.EncodedBuildIdentity(producer)
	testutil.FailErr(t, "encode original producer identity", err)
	var observed *bundled.Manifest
	qualified, err := bundled.FinalizeConsumerArtifact(t.Context(), producer, root, recipe, func(ctx context.Context, prepared *bundled.Manifest) (json.RawMessage, error) {
		observed = prepared
		if _, err := bundled.EncodedBuildIdentity(prepared); err == nil {
			t.Fatal("pending consumer emitted shipping identity")
		}
		if err := bundled.WriteReleaseAudit(ctx, prepared, bundled.EngineBundledDir(root, prepared.OpenGrep.Version), "", filepath.Join(t.TempDir(), "pending.json")); err == nil {
			t.Fatal("pending consumer emitted release audit")
		}
		return buildJSON(t, consumerReport(t, prepared)), nil
	})
	testutil.FailErr(t, "finalize fixture consumer", err)
	if qualified != observed {
		t.Fatal("qualification and sealing changed artifact authority")
	}
	encoded, err := bundled.EncodedBuildIdentity(qualified)
	testutil.FailErr(t, "emit final consumer identity", err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	testutil.FailErr(t, "decode consumer identity", err)
	var identity bundled.BuildIdentity
	testutil.FailErr(t, "parse consumer identity", json.Unmarshal(raw, &identity))
	if identity.SchemaVersion != 2 || identity.Consumer == nil || identity.Consumer.ProducerSHA256 == identity.BinarySHA256 {
		t.Fatal("producer and final binary identities were combined")
	}
	unchanged, err := bundled.EncodedBuildIdentity(producer)
	testutil.FailErr(t, "verify original remains admitted", err)
	if original != unchanged {
		t.Fatal("consumer finalization rewrote producer proof")
	}
	runtimeManifest, err := bundled.ManifestWithBuildIdentity(producer, encoded)
	testutil.FailErr(t, "load runtime consumer authority", err)
	_, err = bundled.VerifyStagedArtifact(runtimeManifest, root, "linux", runtime.GOARCH)
	testutil.FailErr(t, "verify final runtime resources", err)
	directory := bundled.EngineBundledDir(root, producer.OpenGrep.Version)
	audit := filepath.Join(t.TempDir(), "audit.json")
	if err := bundled.WriteReleaseAudit(t.Context(), runtimeManifest, directory, "", audit); err == nil {
		t.Fatal("runtime-only authority replaced original archive admission")
	}
	restored, err := bundled.SelectConsumerArtifact(t.Context(), producer, directory, encoded)
	testutil.FailErr(t, "revalidate producer and finalized package", err)
	testutil.FailErr(t, "write separate consumer audit", bundled.WriteReleaseAudit(t.Context(), restored, directory, "", audit))
	auditRaw, err := os.ReadFile(audit)
	testutil.FailErr(t, "read consumer audit", err)
	var document struct {
		SchemaVersion int             `json:"schema_version"`
		Consumer      json.RawMessage `json:"consumer_finalization"`
	}
	testutil.FailErr(t, "parse consumer audit", json.Unmarshal(auditRaw, &document))
	if document.SchemaVersion != 2 || !bytes.Contains(document.Consumer, []byte("producer_build_identity")) || !bytes.Contains(document.Consumer, []byte("consumer_qualification")) {
		t.Fatal("audit omitted the separate producer and consumer qualification")
	}
	relocated := t.TempDir()
	_, err = bundled.StageArtifact(t.Context(), qualified, relocated, "linux", runtime.GOARCH)
	testutil.FailErr(t, "stage finalized consumer proof with executable", err)
	_, err = bundled.VerifyStagedArtifact(runtimeManifest, relocated, "linux", runtime.GOARCH)
	testutil.FailErr(t, "verify relocated consumer proof", err)
	testutil.FailErr(t, "tamper final executable", os.WriteFile(bundled.ArtifactPath(root, producer.OpenGrep.Version, "linux"), []byte("changed"), 0o755))
	if _, err := bundled.EncodedBuildIdentity(qualified); err == nil {
		t.Fatal("changed final bytes retained shipping authority")
	}
}

func TestConsumerFinalizationRefusesIncompleteOrChangedQualification(t *testing.T) {
	for _, change := range []string{"failed", "unconfined", "wrong digest", "wrong platform", "empty cases", "missing negative", "failed case", "changed final", "canceled", "changed producer payload"} {
		t.Run(change, func(t *testing.T) {
			producer, root, recipe := consumerFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, err := bundled.FinalizeConsumerArtifact(ctx, producer, root, recipe, func(_ context.Context, prepared *bundled.Manifest) (json.RawMessage, error) {
				report := consumerReport(t, prepared)
				switch change {
				case "failed":
					return nil, fmt.Errorf("fixture scanner failed")
				case "unconfined":
					report["confined"] = false
				case "wrong digest":
					report["binary_sha256"] = strings.Repeat("f", 64)
				case "wrong platform":
					report["goarch"] = "other"
				case "empty cases":
					report["cases"] = []any{}
				case "missing negative":
					report["cases"] = report["cases"].([]any)[:1]
				case "failed case":
					report["cases"].([]any)[0].(map[string]any)["passed"] = false
				case "changed final":
					testutil.FailErr(t, "change final artifact during qualification", os.WriteFile(bundled.ArtifactPath(root, producer.OpenGrep.Version, "linux"), []byte("changed"), 0o755))
				case "changed producer payload":
					testutil.FailErr(t, "change copied original proof", os.WriteFile(filepath.Join(bundled.EngineBundledDir(root, producer.OpenGrep.Version), "platform-checks.json"), []byte("changed"), 0o644))
				case "canceled":
					cancel()
				}
				return buildJSON(t, report), nil
			})
			if err == nil {
				t.Fatal("incomplete or changed consumer qualified")
			}
			if _, err := os.Stat(filepath.Join(bundled.EngineBundledDir(root, producer.OpenGrep.Version), "consumer-finalization.json")); !os.IsNotExist(err) {
				t.Fatal("refused qualification published a consumer record")
			}
		})
	}
}

func TestConsumerRecipeRejectsAmbiguousBuildInputs(t *testing.T) {
	_, _, recipe := consumerFixture(t)
	raw := buildJSON(t, recipe)
	_, err := bundled.ParseConsumerRecipe(raw)
	testutil.FailErr(t, "parse closed consumer recipe", err)
	for _, invalid := range [][]byte{append(raw, raw...), bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		bytes.Replace(raw, []byte(`"kind":"nix-elf-relocation"`), []byte(`"kind":"other"`), 1),
		bytes.Replace(raw, []byte(`"source_commit":"`+strings.Repeat("a", 40)+`"`), []byte(`"source_commit":"main"`), 1)} {
		if _, err := bundled.ParseConsumerRecipe(invalid); err == nil {
			t.Fatal("ambiguous consumer recipe accepted")
		}
	}
}
