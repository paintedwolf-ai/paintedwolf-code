package contract

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type releasePlatform struct {
	UpdaterKey   string   `json:"updater_key"`
	ArtifactArch string   `json:"artifact_arch"`
	GOOS         string   `json:"goos"`
	Publication  string   `json:"publication"`
	PackageExts  []string `json:"package_extensions"`
	// UpdaterExt is nil for package-managed platforms, which publish no updater artifact.
	UpdaterExt *string `json:"updater_extension"`
}

type releasePlatformCatalog struct {
	SchemaVersion int               `json:"schema_version"`
	Platforms     []releasePlatform `json:"platforms"`
}

// The release and Linux candidate workflows read the platform catalog rather than
// carrying their own platform lists, and candidates stay isolated from publication.
func TestReleasePlatformCatalogIsClosedAndComplete(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflow := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	for _, needle := range []string{
		`matrix=$(jq -c '{include: [.platforms[] | select(.publication == "public")]}' packaging/release-platforms.json)`,
		"matrix: ${{ fromJSON(needs.classify.outputs.matrix) }}",
		"needs: [classify, build-release]",
		"release-assemble-updater-manifest.py",
		"release-stage-platform-artifacts.sh",
		"matrix.goos == 'linux'",
		"Verify native release runner",
		"release-public-artifacts",
		"cargo install artifact-signing-cli --locked --version 0.11.0",
		"PACKAGE_EXTENSIONS: ${{ join(matrix.package_extensions, ',') }}",
	} {
		if !strings.Contains(workflow, needle) {
			t.Fatalf("release workflow bypasses platform catalog invariant %q", needle)
		}
	}
	if strings.Contains(workflow, "  linux-candidates:") {
		t.Fatal("public Linux releases must not build a second candidate package set")
	}
	candidates := contractcheck.ReadRepoFile(t, root, ".github/workflows/linux-candidates.yml")
	for _, needle := range []string{
		"workflow_call:", "source_sha:", "ref: ${{ inputs.source_sha }}",
		"matrix: ${{ fromJSON(needs.source.outputs.matrix) }}", "./task den:bundle",
		"deb,rpm) ./task den:bundle ;;", "deb,rpm,pkg.tar.zst) ./task den:bundle -- --pacman-container ;;",
		`--package-extensions "${PACKAGE_EXTENSIONS}"`, "PACKAGE_EXTENSIONS: ${{ join(matrix.package_extensions, ',') }}",
		"name: linux-candidate-${{ matrix.updater_key }}",
		"retention-days: 90", "Verify the catalog row for this candidate",
		"CANDIDATE_RUNNER_LABEL:", "./task release:candidate:select", "./task release:rehearse:linux-signing --",
		"./task release:candidate:diagnostics --",
		"profile paintedwolf-candidate-diagnostic /tmp/painted-wolf-linux.*/deb/opt/painted-wolf-code/bin/pw flags=(unconfined)",
		"TMPDIR: /tmp",
	} {
		if !strings.Contains(candidates, needle) {
			t.Fatalf("Linux candidate workflow is missing %q", needle)
		}
	}
	for _, forbidden := range []string{"environment:", "secrets.", "release-r2", "platform-release-", "RUNNER_NAME:",
		"push:", "refs/heads/", "path: .", "python3 .", "python3 scripts/linux_release", "release:live-test",
		"--package-extensions deb,rpm", "PW_PACKAGE_PROBE_LEASE"} {
		if strings.Contains(candidates, forbidden) {
			t.Fatalf("Linux candidate workflow violates its isolation contract (%q)", forbidden)
		}
	}
	if !strings.Contains(extractYAMLJob(workflow, "aggregate-release"), "pattern: platform-release-*") {
		t.Fatal("aggregate-release must collect only signed platform results, never Linux candidates")
	}
}

func TestUpdaterManifestAssemblerRequiresPublicBuildsAndExcludesCandidatePlatforms(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "platforms.json")
	catalog := `{"schema_version":2,"platforms":[` +
		`{"updater_key":"darwin-aarch64","artifact_arch":"aarch64","publication":"public","package_extensions":["dmg"],"updater_extension":"app.tar.gz"},` +
		`{"updater_key":"linux-x86_64","artifact_arch":"x86_64","publication":"candidate","package_extensions":["deb","rpm"],"updater_extension":null},` +
		`{"updater_key":"linux-aarch64","artifact_arch":"aarch64","publication":"candidate","package_extensions":["deb","rpm"],"updater_extension":null},` +
		`{"updater_key":"windows-x86_64","artifact_arch":"x86_64","publication":"candidate","package_extensions":["exe"],"updater_extension":"exe"}]}`
	contractcheck.FailErr(t, "write platform catalog", os.WriteFile(catalogPath, []byte(catalog), 0o644))
	notes := filepath.Join(dir, "notes.md")
	contractcheck.FailErr(t, "write notes", os.WriteFile(notes, []byte("Ready to test.\n"), 0o644))
	fragments := filepath.Join(dir, "fragments")
	contractcheck.FailErr(t, "create fragments", os.Mkdir(fragments, 0o755))
	writeSignedFragment := func(key, extension, signedVersion string) string {
		t.Helper()
		path := filepath.Join(fragments, key+".json")
		value := map[string]any{
			"update_keys":      testUpdateKeyBinding(t, root),
			"platform":         key,
			"signature":        testUpdaterSignature(signedVersion, key),
			"updater_artifact": "painted-wolf-code_v1.0.0-rc.1_" + key + "." + extension,
		}
		raw, err := json.Marshal(value)
		contractcheck.FailErr(t, "encode fragment", err)
		contractcheck.FailErr(t, "write fragment", os.WriteFile(path, raw, 0o644))
		return path
	}
	writeFragment := func(key, extension string) string {
		t.Helper()
		return writeSignedFragment(key, extension, "1.0.0-rc.1")
	}
	darwin := writeFragment("darwin-aarch64", "app.tar.gz")
	output := filepath.Join(dir, "latest.json")
	run := func(wantSuccess bool) {
		t.Helper()
		cmd := exec.Command(
			"python3", filepath.Join(root, "scripts", "release-assemble-updater-manifest.py"),
			"--catalog", catalogPath, "--fragments", fragments,
			"--version", "1.0.0-rc.1", "--notes", notes,
			"--pub-date", "2026-09-03T12:00:00Z",
			"--base-url", "https://downloads.paintedwolf.dev", "--output", output,
		)
		combined, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("assembler success=%v want %v: %v\n%s", err == nil, wantSuccess, err, combined)
		}
	}
	run(true)
	writeFragment("windows-x86_64", "exe")
	run(true)
	// A package-managed platform never contributes an updater fragment.
	linux := writeFragment("linux-x86_64", "deb")
	run(false)
	contractcheck.FailErr(t, "remove linux fragment", os.Remove(linux))
	run(true)
	var manifest struct {
		Platforms map[string]json.RawMessage `json:"platforms"`
	}
	raw, err := os.ReadFile(output)
	contractcheck.FailErr(t, "read updater manifest", err)
	contractcheck.FailErr(t, "decode updater manifest", json.Unmarshal(raw, &manifest))
	if len(manifest.Platforms) != 1 || manifest.Platforms["darwin-aarch64"] == nil {
		t.Fatalf("candidate platforms leaked into updater manifest: %v", manifest.Platforms)
	}
	validate := func(path string, wantSuccess bool, extra ...string) {
		t.Helper()
		args := append([]string{filepath.Join(root, "scripts", "release-validate-updater-manifest.sh"), "--file", path}, extra...)
		cmd := exec.Command("bash", args...)
		cmd.Env = append(os.Environ(), "DOWNLOAD_BASE_URL=https://downloads.paintedwolf.dev")
		combined, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("manifest validation success=%v want %v: %v\n%s", err == nil, wantSuccess, err, combined)
		}
	}
	validate(output, true)
	var served map[string]any
	contractcheck.FailErr(t, "decode served manifest", json.Unmarshal(raw, &served))
	served["platforms"].(map[string]any)["darwin-aarch64"].(map[string]any)["signature"] = testUpdaterSignature("1.0.0", "darwin-aarch64")
	misbound, err := json.Marshal(served)
	contractcheck.FailErr(t, "encode mis-bound manifest", err)
	misboundPath := filepath.Join(dir, "misbound.json")
	contractcheck.FailErr(t, "write mis-bound manifest", os.WriteFile(misboundPath, misbound, 0o644))
	validate(misboundPath, false)
	// A live pointer is read to be replaced, so a mis-bound one must not block that.
	validate(misboundPath, true, "--existing")
	// A withdrawal publishes nothing to install, so halting a mis-bound release works.
	served["withdrawn"] = true
	withdrawn, err := json.Marshal(served)
	contractcheck.FailErr(t, "encode withdrawn manifest", err)
	withdrawnPath := filepath.Join(dir, "withdrawn.json")
	contractcheck.FailErr(t, "write withdrawn manifest", os.WriteFile(withdrawnPath, withdrawn, 0o644))
	validate(withdrawnPath, true)
	// The bundle version omits the prerelease; the signature must carry the release version.
	writeSignedFragment("darwin-aarch64", "app.tar.gz", "1.0.0")
	run(false)
	contractcheck.FailErr(t, "remove darwin fragment", os.Remove(darwin))
	run(false)
	foreign := writeFragment("freebsd-x86_64", "tar.gz")
	run(false)
	contractcheck.FailErr(t, "remove foreign fragment", os.Remove(foreign))
	contractcheck.FailErr(t, "restore darwin fragment", os.WriteFile(darwin, []byte(
		`{"platform":"darwin-aarch64","signature":"signed","updater_artifact":"foreign-file"}`), 0o644))
	run(false)
}

// The GitHub Release is a trailing mirror: it follows immutable publication and
// channel activation, and neither waits on it.
func TestGitHubReleaseMirrorTrailsChannelActivation(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	if _, ok := workflow.Jobs["publish-github-release"]; !ok {
		t.Fatal("release.yml has no publish-github-release job")
	}
	ancestors := workflow.ancestors("publish-github-release")
	for _, job := range []string{"publish-immutable", "activate-updater"} {
		if !ancestors[job] {
			t.Errorf("publish-github-release must follow %s", job)
		}
	}
	for _, job := range []string{"publish-immutable", "activate-updater"} {
		if workflow.ancestors(job)["publish-github-release"] {
			t.Errorf("%s waits on the GitHub Release mirror", job)
		}
	}
}

func TestReleaseSigningAndPublicationCustodyAreSeparated(t *testing.T) {
	t.Parallel()
	workflow := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/release.yml")
	build := extractYAMLJob(workflow, "build-release")
	publish := extractYAMLJob(workflow, "publish-immutable")
	if !strings.Contains(build, "environment: release-signing") {
		t.Fatal("signed platform build must use the release-signing environment")
	}
	for _, credential := range []string{"CLOUDFLARE_API_TOKEN", "HOMEBREW_TAP_TOKEN", "WWW_DISPATCH_TOKEN"} {
		exposure.Absent(t, build, credential)
	}
	if !strings.Contains(publish, "environment: release-publication") {
		t.Fatal("immutable publication must use the release-publication environment")
	}
	if strings.Contains(publish, "platform-release-*") ||
		!strings.Contains(publish, "name: release-public-artifacts") {
		t.Fatal("publication job must receive the catalog-filtered public artifact set, not platform candidates")
	}
	for _, credential := range []string{"APPLE_CERTIFICATE", "AZURE_CLIENT_SECRET", "TAURI_SIGNING_PRIVATE_KEY"} {
		exposure.Absent(t, publish, credential)
	}
	// The feed key signs pointers at publication and never signs code; the artifact key never
	// leaves the signing environment.
	activate := extractYAMLJob(workflow, "activate-updater")
	if !strings.Contains(activate, "FEED_SIGNING_KEYS_JSON: ${{ secrets.FEED_SIGNING_KEYS_JSON }}") ||
		strings.Contains(activate, "TAURI_SIGNING_PRIVATE_KEY") {
		t.Fatal("channel activation must sign pointers with the feed key only")
	}
	if strings.Contains(build, "FEED_SIGNING_KEYS_JSON") {
		t.Fatal("signed build receives the feed signing key")
	}
	halt := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/release-halt.yml")
	if !strings.Contains(halt, "FEED_SIGNING_KEYS_JSON: ${{ secrets.FEED_SIGNING_KEYS_JSON }}") ||
		strings.Contains(halt, "TAURI_SIGNING_PRIVATE_KEY") {
		t.Fatal("release halt must sign replacement pointers with the feed key only")
	}
}

func TestCandidatePlatformsCarryPinnedGitAndOpenGrep(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	var gitPin struct {
		GitVersion string `yaml:"git_version"`
		LFSVersion string `yaml:"lfs_version"`
		Platforms  map[string]struct {
			URL             string `yaml:"url"`
			ReportedVersion string `yaml:"reported_version"`
			SHA256          string `yaml:"sha256"`
		} `yaml:"platforms"`
	}
	contractcheck.FailErr(t, "parse Git engine pin", yaml.Unmarshal(
		[]byte(contractcheck.ReadRepoFile(t, root, "lycaon/config/gitengine/pin.yaml")), &gitPin,
	))
	if gitPin.GitVersion == "" || gitPin.LFSVersion == "" {
		t.Fatal("Git and Git LFS versions must both be pinned")
	}
	const (
		dugite         = "https://github.com/desktop/dugite-native/releases/download/"
		paintedwolfGit = "https://github.com/paintedwolf-ai/paintedwolf-git/releases/download/"
	)
	for key, producer := range map[string]string{
		"darwin-arm64":  dugite,
		"linux-amd64":   paintedwolfGit,
		"linux-arm64":   paintedwolfGit,
		"windows-amd64": dugite,
	} {
		pin, ok := gitPin.Platforms[key]
		sha, shaErr := hex.DecodeString(pin.SHA256)
		if !ok || pin.ReportedVersion == "" || !strings.HasPrefix(pin.URL, producer) || shaErr != nil || len(sha) != 32 {
			t.Fatalf("Git engine platform %q is absent or not immutable: %+v", key, pin)
		}
	}
	if got := gitPin.Platforms["windows-amd64"].ReportedVersion; got != gitPin.GitVersion+".windows.3" {
		t.Fatalf("Windows Git reported version = %q, want an explicit vendor build for %s", got, gitPin.GitVersion)
	}

	var scanners bundled.Manifest
	contractcheck.FailErr(t, "parse bundled scanner release selection", yaml.Unmarshal(
		[]byte(contractcheck.ReadRepoFile(t, root, "lycaon/config/runtime/scanners/bundled-manifest.yaml")), &scanners,
	))
	contractcheck.FailErr(t, "validate immutable scanner release pins", bundled.ValidateManifest(&scanners))
	for _, arch := range platformfloor.DarwinArches() {
		_, err := scanners.ReleaseForPlatform("darwin", arch)
		contractcheck.FailErr(t, "require scanner release for darwin/"+arch, err)
	}

}

// testUpdaterSignature encodes an unverifiable minisign document whose trusted
// comment binds version, matching what `tauri signer sign --app-version` writes.
func testUpdaterSignature(version, file string) string {
	document := "untrusted comment: test signature\n" +
		base64.StdEncoding.EncodeToString(append([]byte("Ed"), make([]byte, 72)...)) + "\n" +
		"trusted comment: timestamp:0\tfile:" + file + "\tversion:" + version + "\n" +
		base64.StdEncoding.EncodeToString(make([]byte, 64)) + "\n"
	return base64.StdEncoding.EncodeToString([]byte(document))
}

func testUpdateKeyBinding(t *testing.T, root string) map[string]any {
	t.Helper()
	cmd := exec.Command("python3", filepath.Join(root, "scripts", "release-metadata.py"))
	raw, err := cmd.Output()
	contractcheck.FailErr(t, "read update key binding", err)
	var metadata map[string]any
	contractcheck.FailErr(t, "decode update key binding", json.Unmarshal(raw, &metadata))
	binding := make(map[string]any)
	for _, key := range []string{"signing_generation", "embedded_generation", "signing_key_fingerprint", "embedded_key_fingerprint"} {
		binding[key] = metadata[key]
	}
	return binding
}
