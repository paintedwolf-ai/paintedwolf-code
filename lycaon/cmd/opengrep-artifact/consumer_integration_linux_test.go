//go:build integration && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/installed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConsumerFinalizationNativeScanner(t *testing.T) {
	input := os.Getenv("PW_SCANNER_CONSUMER_RECIPE")
	if input == "" {
		testutil.MissingScannerResource(t, "native consumer relocation recipe", fmt.Errorf("PW_SCANNER_CONSUMER_RECIPE is required for consumer qualification"))
	}
	raw, err := readConsumerInput(input, 64<<10)
	testutil.FailErr(t, "read native relocation recipe", err)
	recipe, err := bundled.ParseConsumerRecipe(raw)
	testutil.FailErr(t, "parse declared native relocation inputs", err)
	source, err := bundled.LoadManifest()
	testutil.FailErr(t, "load production source selection", err)
	producerDirectory := os.Getenv("PW_SCANNER_PRODUCER_DIRECTORY")
	if !filepath.IsAbs(producerDirectory) {
		t.Fatal("PW_SCANNER_PRODUCER_DIRECTORY must identify an absolute admitted original")
	}
	producer, err := bundled.SelectReleaseArtifact(t.Context(), source, producerDirectory, "linux", runtime.GOARCH)
	testutil.FailErr(t, "independently admit original pinned producer", err)
	originalIdentity, err := bundled.EncodedBuildIdentity(producer)
	testutil.FailErr(t, "retain original authority", err)
	root := t.TempDir()
	binary, err := bundled.StageArtifact(t.Context(), producer, root, "linux", runtime.GOARCH)
	testutil.FailErr(t, "stage private scanner copy", err)
	testutil.FailErr(t, "verify declared relocation tool", bundled.VerifyFileSHA256(recipe.ToolPath, recipe.ToolSHA256))
	recipe.Arguments = append(recipe.Arguments, binary)
	patch := exec.CommandContext(t.Context(), recipe.ToolPath, recipe.Arguments...)
	output, err := patch.CombinedOutput()
	if err != nil {
		t.Fatalf("relocate private scanner: %v\n%s", err, output)
	}
	raw, err = json.Marshal(recipe)
	testutil.FailErr(t, "encode executed relocation recipe", err)
	recipePath := filepath.Join(t.TempDir(), "executed-recipe.json")
	testutil.FailErr(t, "retain executed relocation inputs", os.WriteFile(recipePath, raw, 0o600))
	evidence, err := filepath.Abs("../../config/runtime/scanners")
	testutil.FailErr(t, "resolve committed producer selection evidence", err)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	encoded, err := execute(options{mode: "finalize-consumer", directory: producerDirectory, root: root,
		consumerRecipe: recipePath, evidenceRoot: evidence, auditOutput: filepath.Join(t.TempDir(), "consumer-audit.json")})
	testutil.FailErr(t, "qualify actual transformed scanner through build CLI", err)
	finalDirectory := bundled.EngineBundledDir(root, source.OpenGrep.Version)
	selected, err := bundled.SelectConsumerArtifact(t.Context(), producer, finalDirectory, encoded)
	testutil.FailErr(t, "admit final executable independently from producer", err)
	runtimeManifest, err := bundled.ManifestWithBuildIdentity(source, encoded)
	testutil.FailErr(t, "bind emitted final shipping authority", err)
	assemblerRoot := filepath.Join(t.TempDir(), "lib", "painted-wolf-code", "engine-root")
	assembled, err := bundled.StageArtifact(t.Context(), selected, assemblerRoot, "linux", runtime.GOARCH)
	testutil.FailErr(t, "assemble every finalized resource without transformation", err)
	report, err := installed.VerifyArtifact(t.Context(), assemblerRoot, runtimeManifest)
	if err != nil || report.Status != "passed" || !report.Confined {
		t.Fatalf("assembled final scanner report=%+v error=%v", report, err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.txt")
	testutil.FailErr(t, "retain returned build identity", os.WriteFile(identityFile, []byte(encoded+"\n"), 0o600))
	audit := options{mode: "audit", directory: producerDirectory, packagedDirectory: filepath.Dir(assembled),
		consumerIdentity: identityFile, evidenceRoot: evidence, auditOutput: filepath.Join(t.TempDir(), "assembled-audit.json")}
	_, err = execute(audit)
	testutil.FailErr(t, "audit final assembled bytes against original and consumer authority", err)
	qualifiedRaw, err := base64.StdEncoding.DecodeString(encoded)
	testutil.FailErr(t, "decode final embedded identity", err)
	var identity bundled.BuildIdentity
	testutil.FailErr(t, "read final embedded identity", json.Unmarshal(qualifiedRaw, &identity))
	if identity.SchemaVersion != 2 || identity.Consumer == nil || identity.BinarySHA256 == identity.Consumer.ProducerSHA256 {
		t.Fatal("transformed identity lost its separate original producer")
	}
	t.Logf("qualified native %s scanner: original=%s final=%s; %d assembled product cases", runtime.GOARCH, identity.Consumer.ProducerSHA256, identity.BinarySHA256, len(report.Cases))
	for _, name := range []string{"opengrep", "consumer-finalization.json", "platform-checks.json"} {
		t.Run("changed "+name, func(t *testing.T) {
			path := filepath.Join(filepath.Dir(assembled), name)
			original, err := os.ReadFile(path)
			testutil.FailErr(t, "read assembled resource", err)
			changed := append(append([]byte{}, original...), '\n')
			testutil.FailErr(t, "alter final packaged resource", os.WriteFile(path, changed, 0o755))
			if _, err := execute(audit); err == nil {
				t.Fatal("changed assembled resource retained final audit authority")
			}
			observed, err := os.ReadFile(path)
			testutil.FailErr(t, "inspect refused packaged resource", err)
			if !bytes.Equal(observed, changed) {
				t.Fatal("audit repaired or replaced the refused package")
			}
			testutil.FailErr(t, "restore only fixture-owned resource", os.WriteFile(path, original, 0o755))
		})
	}
	unchanged, err := bundled.EncodedBuildIdentity(producer)
	testutil.FailErr(t, "reverify unchanged producer archive and payload", err)
	if unchanged != originalIdentity {
		t.Fatal("consumer qualification mutated original producer evidence")
	}
	finalBytes, err := os.ReadFile(assembled)
	testutil.FailErr(t, "read final scanner bytes", err)
	digest := sha256.Sum256(finalBytes)
	if hex.EncodeToString(digest[:]) != report.BinarySHA256 {
		t.Fatal("final assembly changed the qualified executable")
	}
}
