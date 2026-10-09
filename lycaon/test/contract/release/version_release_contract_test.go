package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestVersionFileSemver(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	contractcheck.FailErr(t, "read VERSION", err)
	ver := strings.TrimSpace(string(raw))
	cmd := exec.Command("python3", filepath.Join(root, "scripts", "semver-compare.py"), "eq", ver, ver)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("VERSION = %q is not complete SemVer: %v\n%s", ver, err, output)
	}
}

func TestReleaseMetadataProjectsSemverAndNativeVersions(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	cases := []struct {
		version, channel, native, windowsPackage string
		prerelease                               bool
	}{
		{"1.0.0-rc.1", "preview", "1.0.0", "1.0.42", true},
		{"1.0.0", "stable", "1.0.0", "1.0.42", false},
		{"2.7.3-beta.4+ci.9", "preview", "2.7.3", "2.7.42", true},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			dir := t.TempDir()
			contractcheck.FailErr(t, "write VERSION", os.WriteFile(filepath.Join(dir, "VERSION"), []byte(tc.version+"\n"), 0o644))
			contractcheck.FailErr(t, "write RELEASE_BUILD", os.WriteFile(filepath.Join(dir, "RELEASE_BUILD"), []byte("42\n"), 0o644))
			contractcheck.FailErr(t, "create packaging", os.Mkdir(filepath.Join(dir, "packaging"), 0o755))
			keys, err := os.ReadFile(filepath.Join(root, "packaging", "update-keys.json"))
			contractcheck.FailErr(t, "read update keys", err)
			contractcheck.FailErr(t, "write update keys", os.WriteFile(filepath.Join(dir, "packaging", "update-keys.json"), keys, 0o644))
			cmd := exec.Command("python3", filepath.Join(root, "scripts", "release-metadata.py"), "--root", dir)
			out, err := cmd.Output()
			contractcheck.FailErr(t, "derive release metadata", err)
			var got struct {
				ProductVersion        string `json:"product_version"`
				Channel               string `json:"channel"`
				NativeVersion         string `json:"native_version"`
				WindowsPackageVersion string `json:"windows_package_version"`
				GitHubPrerelease      bool   `json:"github_prerelease"`
			}
			contractcheck.FailErr(t, "decode release metadata", json.Unmarshal(out, &got))
			if got.ProductVersion != tc.version || got.Channel != tc.channel ||
				got.NativeVersion != tc.native || got.WindowsPackageVersion != tc.windowsPackage ||
				got.GitHubPrerelease != tc.prerelease {
				t.Fatalf("metadata = %+v", got)
			}
		})
	}
}

func TestReleaseMetadataRejectsNonPortableBuildNumbers(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	for _, build := range []string{"0", "01", "65536", "candidate"} {
		dir := t.TempDir()
		contractcheck.FailErr(t, "write VERSION", os.WriteFile(filepath.Join(dir, "VERSION"), []byte("1.0.0\n"), 0o644))
		contractcheck.FailErr(t, "write RELEASE_BUILD", os.WriteFile(filepath.Join(dir, "RELEASE_BUILD"), []byte(build+"\n"), 0o644))
		cmd := exec.Command("python3", filepath.Join(root, "scripts", "release-metadata.py"), "--root", dir)
		if err := cmd.Run(); err == nil {
			t.Fatalf("RELEASE_BUILD=%q was accepted", build)
		}
	}
}

func TestReleaseSemverPrecedenceCoversCandidatePromotion(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0-rc.2",
		"1.0.0",
		"1.0.1-rc.1",
	}
	for index := 1; index < len(ordered); index++ {
		cmd := exec.Command(
			"python3", filepath.Join(root, "scripts", "semver-compare.py"),
			"gt", ordered[index], ordered[index-1],
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s must be newer than %s: %v\n%s", ordered[index], ordered[index-1], err, output)
		}
	}
	cmd := exec.Command(
		"python3", filepath.Join(root, "scripts", "semver-compare.py"),
		"eq", "1.0.0+build.2", "1.0.0+build.1",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build metadata must not change precedence: %v\n%s", err, output)
	}
	cmd = exec.Command(
		"python3", filepath.Join(root, "scripts", "semver-compare.py"),
		"eq", "v1.0.0", "v1.0.0",
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("product versions accepted a tag prefix")
	}
}

func TestReleaseWorkflowInputsMatchContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflow := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")

	collect := func(pattern string) []string {
		t.Helper()
		matches := regexp.MustCompile(pattern).FindAllStringSubmatch(workflow, -1)
		seen := make(map[string]struct{}, len(matches))
		for _, match := range matches {
			seen[match[1]] = struct{}{}
		}
		got := make([]string, 0, len(seen))
		for name := range seen {
			got = append(got, name)
		}
		slices.Sort(got)
		return got
	}
	wantSecrets := []string{
		"APPLE_API_ISSUER",
		"APPLE_API_KEY_ID",
		"APPLE_API_KEY_P8",
		"APPLE_CERTIFICATE",
		"APPLE_CERTIFICATE_PASSWORD",
		"APPLE_ENGINE_PROVISIONING_PROFILE_BASE64",
		"APPLE_SIGNING_IDENTITY",
		"AZURE_CLIENT_ID",
		"AZURE_CLIENT_SECRET",
		"AZURE_TENANT_ID",
		"CLOUDFLARE_ACCOUNT_ID",
		"CLOUDFLARE_API_TOKEN",
		"FEED_SIGNING_KEYS_JSON",
		"HOMEBREW_TAP_TOKEN",
		"R2_BUCKET",
		"TAURI_SIGNING_PRIVATE_KEY",
		"TAURI_SIGNING_PRIVATE_KEY_PASSWORD",
		"WWW_DISPATCH_TOKEN",
	}
	wantVariables := []string{
		"AZURE_ARTIFACT_SIGNING_ACCOUNT",
		"AZURE_ARTIFACT_SIGNING_ENDPOINT",
		"AZURE_ARTIFACT_SIGNING_PROFILE",
		"DOWNLOAD_BASE_URL",
		"HOMEBREW_TAP_REPO",
	}
	slices.Sort(wantSecrets)
	slices.Sort(wantVariables)
	if got := collect(`secrets\.([A-Z][A-Z0-9_]*)`); !slices.Equal(got, wantSecrets) {
		t.Fatalf("release workflow secrets = %v; contract = %v", got, wantSecrets)
	}
	if got := collect(`vars\.([A-Z][A-Z0-9_]*)`); !slices.Equal(got, wantVariables) {
		t.Fatalf("release workflow variables = %v; contract = %v", got, wantVariables)
	}
}

func TestDevelopmentSigningIdentityHasOneSource(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	config := contractcheck.ReadRepoFile(t, root, "scripts/dev-signing-config.sh")
	declarations := regexp.MustCompile(`(?m)^readonly DEV_SIGNING_(?:IDENTITY|IDENTIFIER)="([^"]+)"$`).FindAllStringSubmatch(config, -1)
	if len(declarations) != 2 {
		t.Fatalf("dev-signing-config.sh has %d signing declarations; want 2", len(declarations))
	}
	for _, declaration := range declarations {
		value := declaration[1]
		for _, rel := range []string{"scripts/dev-signing-identity.sh", "scripts/sign-dev-binary.sh"} {
			body := contractcheck.ReadRepoFile(t, root, rel)
			if strings.Contains(body, value) {
				t.Fatalf("%s duplicates signing identity value %q", rel, value)
			}
			if !strings.Contains(body, "dev-signing-config.sh") {
				t.Fatalf("%s does not source dev-signing-config.sh", rel)
			}
		}
	}
}

func TestReleaseSourceValidationAcceptsRootCommitAndRequiresIncreasingBuild(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	repo := t.TempDir()
	keys, err := os.ReadFile(filepath.Join(root, "packaging", "update-keys.json"))
	contractcheck.FailErr(t, "read release key registry", err)
	contractcheck.FailErr(t, "create packaging fixture", os.MkdirAll(filepath.Join(repo, "packaging"), 0o755))
	contractcheck.FailErr(t, "write release key registry", os.WriteFile(filepath.Join(repo, "packaging", "update-keys.json"), keys, 0o644))
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("git", "init", "-b", "main")
	run("git", "config", "user.name", "Release Test")
	run("git", "config", "user.email", "release@example.com")
	contractcheck.FailErr(t, "write VERSION", os.WriteFile(filepath.Join(repo, "VERSION"), []byte("0.9.0\n"), 0o644))
	contractcheck.FailErr(t, "write RELEASE_BUILD", os.WriteFile(filepath.Join(repo, "RELEASE_BUILD"), []byte("1\n"), 0o644))
	run("git", "add", "VERSION", "RELEASE_BUILD", "packaging/update-keys.json")
	run("git", "commit", "-m", "Preview release")
	script := filepath.Join(root, "scripts", "release-source-validate.sh")
	if output := run("bash", script, "v0.9.0", run("git", "rev-parse", "HEAD"), "refs/heads/main"); output != "" {
		t.Fatalf("root commit validation produced diagnostics: %s", output)
	}
	run("git", "tag", "v0.9.0")
	contractcheck.FailErr(t, "write VERSION", os.WriteFile(filepath.Join(repo, "VERSION"), []byte("1.0.0\n"), 0o644))
	contractcheck.FailErr(t, "write RELEASE_BUILD", os.WriteFile(filepath.Join(repo, "RELEASE_BUILD"), []byte("2\n"), 0o644))
	run("git", "add", "VERSION", "RELEASE_BUILD")
	run("git", "commit", "-m", "Initial public release")
	sha := run("git", "rev-parse", "HEAD")
	run("bash", script, "v1.0.0", sha, "refs/heads/main")
	run("git", "tag", "v1.0.0")

	contractcheck.FailErr(t, "write VERSION", os.WriteFile(filepath.Join(repo, "VERSION"), []byte("1.0.1-rc.1\n"), 0o644))
	run("git", "add", "VERSION")
	run("git", "commit", "-m", "Candidate with stale build number")
	sha = run("git", "rev-parse", "HEAD")
	cmd := exec.Command("bash", script, "v1.0.1-rc.1", sha, "refs/heads/main")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "must exceed prior release build") {
		t.Fatalf("stale release build was accepted: %v\n%s", err, output)
	}
}

func TestProductAndNativeVersionProjectionsAreSynchronized(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cmd := exec.Command("python3", filepath.Join(root, "scripts", "release-metadata.py"))
	out, err := cmd.Output()
	contractcheck.FailErr(t, "derive release metadata", err)
	var metadata struct {
		ProductVersion     string `json:"product_version"`
		NativeVersion      string `json:"native_version"`
		MacOSBundleVersion string `json:"macos_bundle_version"`
	}
	contractcheck.FailErr(t, "decode release metadata", json.Unmarshal(out, &metadata))
	var packageJSON, tauriJSON map[string]any
	contractcheck.FailErr(t, "parse package.json", json.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, "lycaon-den/package.json")), &packageJSON))
	contractcheck.FailErr(t, "parse tauri.conf.json", json.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, "lycaon-den/src-tauri/tauri.conf.json")), &tauriJSON))
	macos := tauriJSON["bundle"].(map[string]any)["macOS"].(map[string]any)
	if packageJSON["version"] != metadata.ProductVersion ||
		tauriJSON["version"] != metadata.NativeVersion ||
		macos["bundleVersion"] != metadata.MacOSBundleVersion {
		t.Fatalf("version projections drifted: package=%v tauri=%v bundle=%v metadata=%+v",
			packageJSON["version"], tauriJSON["version"], macos["bundleVersion"], metadata)
	}
}

func TestReleaseWorkflowValidatesTagAgainstVERSION(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	contractcheck.FailErr(t, "read release.yml", err)
	body := string(raw)
	for _, needle := range []string{
		"Validate release tag and source",
		"fetch-depth: 0",
		"scripts/release-source-validate.sh",
		"refs/remotes/origin/main",
		"./task release:preflight -- --require-corpus",
		"./task upgrade:rehearse",
		"aggregate-release:",
		"packaging/release-platforms.json",
		"release-assemble-updater-manifest.py",
		"publish-immutable:",
		"activate-updater:",
		"publish-github-release:",
		"updates/releases/${VERSION}.json",
		"--channel \"${CHANNEL}\"",
		"--prerelease",
		"--latest=false",
		"scripts/release-validate-updater-manifest.sh",
		"environment: release-publication",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("release.yml missing %q", needle)
		}
	}
	if strings.Contains(body, "run: ./task check-fast") {
		t.Fatal("release.yml may not substitute check-fast for the full release gate")
	}
	gateAt := strings.Index(body, "ship-gates:")
	provenanceAt := strings.Index(body, "scripts/release-source-validate.sh")
	signingAt := strings.Index(body, "name: Import signing certificate")
	publishAt := strings.Index(body, "name: Publish immutable release objects")
	if provenanceAt > gateAt || gateAt > publishAt {
		t.Fatal("release.yml must prove exact-main provenance and pass the full gate before publishing")
	}
	aggregateAt := strings.Index(body, "aggregate-release:")
	activateAt := strings.Index(body, "activate-updater:")
	if !(signingAt < aggregateAt && aggregateAt < publishAt && publishAt < activateAt) {
		t.Fatal("release.yml must sign → aggregate → publish immutable objects → activate")
	}
	if _, err := os.Stat(filepath.Join(root, "scripts/sync-den-versions.sh")); err != nil {
		t.Fatalf("sync-den-versions.sh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts/release-validate-updater-manifest.sh")); err != nil {
		t.Fatalf("release-validate-updater-manifest.sh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts/release-source-validate.sh")); err != nil {
		t.Fatalf("release-source-validate.sh: %v", err)
	}
}
