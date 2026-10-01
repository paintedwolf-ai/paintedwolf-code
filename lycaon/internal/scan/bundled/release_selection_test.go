package bundled_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func mockedVerification(t *testing.T, tag, digest string, workflow bool) []byte {
	t.Helper()
	certificate := map[string]string{"subjectAlternativeName": "https://dotcom.releases.github.com"}
	if workflow {
		identity := "https://github.com/paintedwolf-ai/paintedwolf-opengrep/.github/workflows/native-release.yml@refs/tags/" + tag
		certificate = map[string]string{
			"issuer": "https://token.actions.githubusercontent.com", "subjectAlternativeName": identity, "buildSignerURI": identity,
			"buildSignerDigest": strings.Repeat("a", 40), "runnerEnvironment": "github-hosted",
			"sourceRepositoryURI": "https://github.com/paintedwolf-ai/paintedwolf-opengrep", "sourceRepositoryDigest": strings.Repeat("a", 40),
			"sourceRepositoryRef": "refs/tags/" + tag, "sourceRepositoryIdentifier": "1362758908", "sourceRepositoryOwnerIdentifier": "289211278",
		}
	}
	proof := map[string]any{
		"attestation": map[string]any{"bundle": map[string]any{"mediaType": "mocked-verifier-output"}},
		"verificationResult": map[string]any{
			"signature": map[string]any{"certificate": certificate}, "verifiedTimestamps": []any{map[string]any{"timestamp": "2026-09-09T00:00:00Z"}},
			"statement": map[string]any{"predicateType": "https://slsa.dev/provenance/v1", "subject": []any{map[string]any{"digest": map[string]string{"sha256": digest}}}},
		},
	}
	if workflow {
		return buildJSON(t, []any{proof})
	}
	return buildJSON(t, proof)
}

const mockedGitHubVerifier = `#!/bin/sh
printf '%s\n' "$*" >> "$GH_TEST_DIRECTORY/commands"
if [ "$1" = api ]; then
  case "$4" in
    repos/*) /bin/cat "$GH_TEST_DIRECTORY/repository.json" ;;
    *) /bin/cat "$GH_TEST_DIRECTORY/release-state.json" ;;
  esac
elif [ "$1" = attestation ]; then
  identities=0
  for argument in "$@"; do
    case "$argument" in
      --cert-identity|--cert-identity-regex|--signer-repo|--signer-workflow) identities=$((identities + 1)) ;;
    esac
  done
  [ "$identities" -eq 1 ] || exit 2
  case "$3" in
    */release.json) subject=descriptor ;;
    *) subject=archive ;;
  esac
  if [ "$GH_TEST_FAILURE" = "$subject" ]; then exit 1; fi
  /bin/cat "$GH_TEST_DIRECTORY/$subject-workflow.json"
else
  case "$4" in
    */release.json) subject=descriptor ;;
    *) subject=archive ;;
  esac
  /bin/cat "$GH_TEST_DIRECTORY/$subject-release.json"
fi
`

func TestAuthenticatedSelectionPreservesPinUntilAllProofsPass(t *testing.T) {
	for _, scenario := range []string{"success", "descriptor", "archive", "mutable release", "replacement repository", "missing receipt directory", "modified cached archive", "wrong release subject", "existing receipt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReleaseFixture(t)
			directory := t.TempDir()
			tag := "v" + f.source.OpenGrep.Version
			f.source.OpenGrep.Artifacts[0].URL = "https://github.com/paintedwolf-ai/paintedwolf-opengrep/releases/download/" + tag + "/opengrep.tar.gz"
			descriptor := buildJSON(t, f.source)
			writeBuildFile(t, directory, "release.json", descriptor)
			writeBuildFile(t, directory, "gh", []byte(mockedGitHubVerifier))
			testutil.FailErr(t, "make mocked verifier executable", os.Chmod(filepath.Join(directory, "gh"), 0o700))
			writeBuildFile(t, directory, "repository.json", []byte(`{"id":1362758908,"owner":{"id":289211278}}`))
			state := map[string]any{"id": 123, "tag_name": tag, "draft": false, "immutable": true}
			if scenario == "mutable release" {
				state["immutable"] = false
			}
			writeBuildFile(t, directory, "release-state.json", buildJSON(t, state))
			if scenario == "replacement repository" {
				writeBuildFile(t, directory, "repository.json", []byte(`{"id":1,"owner":{"id":289211278}}`))
			}
			for name, digest := range map[string]string{"descriptor": buildDigest(descriptor), "archive": f.source.OpenGrep.Artifacts[0].SHA256} {
				writeBuildFile(t, directory, name+"-workflow.json", mockedVerification(t, tag, digest, true))
				writeBuildFile(t, directory, name+"-release.json", mockedVerification(t, tag, digest, false))
			}
			if scenario == "wrong release subject" {
				writeBuildFile(t, directory, "archive-release.json", mockedVerification(t, tag, strings.Repeat("e", 64), false))
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GH_TEST_DIRECTORY", directory)
			t.Setenv("GH_TEST_FAILURE", scenario)
			if scenario == "existing receipt" {
				t.Setenv("GH_TEST_FAILURE", "descriptor")
			}
			output := filepath.Join(directory, "pin.yaml")
			writeBuildFile(t, directory, "pin.yaml", []byte("reviewed existing pin\n"))
			if scenario == "existing receipt" {
				testutil.FailErr(t, "create receipt fixture directory", os.MkdirAll(filepath.Join(directory, "opengrep-attestations"), 0o700))
				writeBuildFile(t, directory, "opengrep-attestations/"+buildDigest(descriptor)+".json", []byte(`{"verified":true}`))
			}
			if scenario == "missing receipt directory" {
				writeBuildFile(t, directory, "opengrep-attestations", []byte("not a directory"))
			}
			if scenario == "modified cached archive" {
				testutil.FailErr(t, "alter archive bytes", os.WriteFile(f.archive, []byte("changed"), 0o600))
			}
			err := bundled.SelectAttestedRelease(t.Context(), bundled.AttestedReleaseSelection{
				Descriptor: filepath.Join(directory, "release.json"), Tag: tag, ExpectedCommit: strings.Repeat("a", 40), Destination: output,
				GOOS: f.goos, GOARCH: f.goarch, Fetch: bundled.ReleaseFetchOptions{CacheRoot: filepath.Join(directory, "cache"), Archive: f.archive},
			})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("selection outcome=%v", err)
			}
			raw, readErr := os.ReadFile(output)
			testutil.FailErr(t, "read selected pin", readErr)
			if scenario != "success" {
				if string(raw) != "reviewed existing pin\n" {
					t.Fatal("failed authentication changed reviewed pin")
				}
				return
			}
			if !strings.Contains(string(raw), f.source.OpenGrep.Artifacts[0].SHA256) {
				t.Fatal("qualified selection lost archive pin")
			}
			receipt, readErr := os.ReadFile(filepath.Join(directory, "opengrep-attestations", buildDigest(descriptor)+".json"))
			testutil.FailErr(t, "read retained cryptographic evidence", readErr)
			var evidence map[string]any
			testutil.FailErr(t, "parse evidence", json.Unmarshal(receipt, &evidence))
			if evidence["expected_commit"] != strings.Repeat("a", 40) || len(evidence["artifacts"].([]any)) != 1 {
				t.Fatal("evidence omitted independent identity or subject")
			}
			var boundDescriptor struct {
				Descriptor []byte `json:"descriptor_bytes"`
			}
			testutil.FailErr(t, "decode exact descriptor bytes", json.Unmarshal(receipt, &boundDescriptor))
			if buildDigest(boundDescriptor.Descriptor) != buildDigest(descriptor) {
				t.Fatal("receipt reformatted authenticated descriptor bytes")
			}
			commands, readErr := os.ReadFile(filepath.Join(directory, "commands"))
			testutil.FailErr(t, "read verifier invocations", readErr)
			if strings.Count(string(commands), "--deny-self-hosted-runners") != 2 || strings.Count(string(commands), "--source-digest "+strings.Repeat("a", 40)) != 2 {
				t.Fatalf("verification policy omitted: %s", commands)
			}
			checkSelectedReleaseAudit(t, f, directory, raw)
		})
	}
}

func checkSelectedReleaseAudit(t *testing.T, f releaseFixture, directory string, selectedYAML []byte) {
	t.Helper()
	var selected bundled.Manifest
	testutil.FailErr(t, "decode selected consumer manifest", yaml.Unmarshal(selectedYAML, &selected))
	if selected.OpenGrepSelection == nil {
		t.Fatal("selection omitted evidence reference")
	}
	f.source = &selected
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: filepath.Join(directory, "cache"), Offline: true})
	output := filepath.Join(directory, "app-audit.json")
	testutil.FailErr(t, "emit offline authenticated audit", bundled.WriteReleaseAudit(t.Context(), resolved.Manifest, f.directory, directory, output))
	checkAuditMode(t, output, "authenticated-selection")
	proof := *resolved.Manifest.OpenGrepSelection
	if proof.Tag != "v"+selected.OpenGrep.Version || proof.ProducerCommit != strings.Repeat("a", 40) {
		t.Fatal("audit source reference drifted")
	}
	for _, scenario := range []string{"missing evidence", "altered evidence", "changed descriptor selection", "stale packaged engine"} {
		t.Run(scenario, func(t *testing.T) {
			m := *resolved.Manifest
			m.OpenGrep.Artifacts = append([]bundled.ReleaseArtifact(nil), m.OpenGrep.Artifacts...)
			root := directory
			packaged := f.directory
			switch scenario {
			case "missing evidence":
				root = t.TempDir()
			case "altered evidence":
				root = t.TempDir()
				testutil.FailErr(t, "create altered evidence directory", os.Mkdir(filepath.Join(root, "opengrep-attestations"), 0o700))
				writeBuildFile(t, root, "opengrep-attestations/"+proof.DescriptorSHA256+".json", []byte(`{"verified":true}`))
			case "changed descriptor selection":
				m.OpenGrep.Artifacts[0].URL += "?other"
			case "stale packaged engine":
				packaged = t.TempDir()
				entries, err := os.ReadDir(f.directory)
				testutil.FailErr(t, "read packaged fixture", err)
				for _, entry := range entries {
					raw, err := os.ReadFile(filepath.Join(f.directory, entry.Name()))
					testutil.FailErr(t, "copy packaged payload", err)
					if entry.Name() == "opengrep" {
						raw = append(raw, 'x')
					}
					writeBuildFile(t, packaged, entry.Name(), raw)
				}
			}
			failedOutput := filepath.Join(t.TempDir(), "audit.json")
			if err := bundled.WriteReleaseAudit(t.Context(), &m, packaged, root, failedOutput); err == nil {
				t.Fatal("audit silently accepted or downgraded missing authentication")
			}
			if _, err := os.Stat(failedOutput); !os.IsNotExist(err) {
				t.Fatal("failed audit published output")
			}
		})
	}
}

func checkAuditMode(t *testing.T, path, mode string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read app release audit", err)
	var audit struct {
		Mode      string                             `json:"mode"`
		Evidence  []byte                             `json:"evidence_bytes"`
		Selection *bundled.ReleaseSelectionReference `json:"selection"`
	}
	testutil.FailErr(t, "decode app audit", json.Unmarshal(raw, &audit))
	if audit.Mode != mode {
		t.Fatalf("audit mode=%q", audit.Mode)
	}
	if mode == "authenticated-selection" && (audit.Selection == nil || buildDigest(audit.Evidence) != audit.Selection.EvidenceSHA256) {
		t.Fatal("audit lost original authentication evidence bytes")
	}
	if mode == "reviewed-pin-only" && (audit.Selection != nil || len(audit.Evidence) != 0) {
		t.Fatal("reviewed pin acquired invented authentication")
	}
}

func TestReviewedPinAuditNeedsNoAttestationOrNetwork(t *testing.T) {
	f := newReleaseFixture(t)
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive, Offline: true})
	output := filepath.Join(t.TempDir(), "audit.json")
	t.Setenv("PATH", t.TempDir())
	testutil.FailErr(t, "audit reviewed pin offline", bundled.WriteReleaseAudit(t.Context(), resolved.Manifest, f.directory, "", output))
	checkAuditMode(t, output, "reviewed-pin-only")
}

func TestProducerDescriptorCannotSupplyConsumerEvidenceReference(t *testing.T) {
	t.Parallel()
	raw := buildJSON(t, sourceManifest())
	raw = append([]byte(`{"opengrep_selection":{"tag":"forged"},`), raw[1:]...)
	if _, err := bundled.ParseReleaseManifest(raw); err == nil {
		t.Fatal("producer supplied consumer trust reference")
	}
}
