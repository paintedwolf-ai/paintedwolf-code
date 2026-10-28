package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
		"AUR_SSH_PRIVATE_KEY",
		"AZURE_CLIENT_ID",
		"AZURE_CLIENT_SECRET",
		"AZURE_TENANT_ID",
		"CLOUDFLARE_ACCOUNT_ID",
		"CLOUDFLARE_API_TOKEN",
		"FEED_SIGNING_KEYS_JSON",
		"HOMEBREW_TAP_TOKEN",
		"LINUX_PACKAGE_SIGNING_KEY",
		"NIX_CACHE_SIGNING_KEY",
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
		t.Fatal("release.yml must prove landed-source provenance and pass the full gate before publishing")
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
