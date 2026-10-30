package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRepositorySetupArtifactsFollowSharedReleaseAdmission(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	if _, duplicate := workflow.Jobs["linux-candidates"]; duplicate {
		t.Fatal("public Linux release must build one package set")
	}
	native := workflow.Jobs["linux-repositories"]
	if native.Environment != "release-signing" {
		t.Fatal("repository setup must use signing custody")
	}
	assembled, uploaded := false, false
	for _, step := range native.Steps {
		if strings.Contains(step.Run, "scripts.linux_release.release_repository_setup") {
			if step.Env["LINUX_PACKAGE_SIGNING_KEY"] != "${{ secrets.LINUX_PACKAGE_SIGNING_KEY }}" {
				t.Fatal("setup assembly must use the selected package signing key")
			}
			assembled = true
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["name"] == "linux-repository-setup" {
			if !assembled || !strings.Contains(step.With["path"], "linux-setup/*") {
				t.Fatal("setup upload must follow independent admission and contain its curated assets")
			}
			uploaded = true
		}
	}
	if !assembled || !uploaded || !workflow.ancestors("publication-credentials")["linux-repositories"] {
		t.Fatal("setup packages must be assembled and admitted before publication credentials")
	}
	for _, name := range []string{"publish-artifacts", "publish-github-release"} {
		job := workflow.Jobs[name]
		if !workflow.ancestors(name)["publication-credentials"] {
			t.Errorf("%s bypasses shared publication admission", name)
		}
		downloaded := false
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With["name"] == "linux-repository-setup" {
				downloaded = step.With["path"] == "release-artifacts"
			}
		}
		loops := 1
		if name == "publish-artifacts" {
			loops = 2
		}
		if !downloaded || strings.Count(job.text(), "for artifact in release-artifacts/*; do") != loops {
			t.Errorf("%s must deposit or compare every curated setup asset", name)
		}
	}
	if !strings.Contains(workflow.Jobs["publish-github-release"].text(), `gh release create "v${VERSION}" release-artifacts/*`) {
		t.Fatal("the trailing mirror must include every curated setup asset")
	}
}

func TestReleaseDistributionDocumentFollowsObservedDeliveryBeforeAnnouncement(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	for _, completed := range []string{"ship-gates", "publish-artifacts", "publish-linux-repositories", "linux-delivery"} {
		if !workflow.ancestors("publish-immutable")[completed] {
			t.Errorf("distribution signing precedes completed %s", completed)
		}
	}
	metadata := workflow.Jobs["publish-immutable"]
	if metadata.Environment != "release-publication" || !strings.Contains(metadata.If, "needs.classify.outputs.publish == 'true'") {
		t.Fatal("completed metadata publication must use the shared publication decision and custody")
	}
	for _, artifact := range []string{"release-public-artifacts", "linux-repository-setup", "release-metadata", "linux-repository-publication", "nix-cache-publication", "release-channel-plan"} {
		if !slices.Contains(metadata.artifacts("download-artifact"), artifact) {
			t.Errorf("completed document lacks selected %s", artifact)
		}
	}
	text := metadata.text()
	prepare := strings.Index(text, "scripts.release_distribution_contract.release_lane")
	deposit := strings.Index(text, "bash scripts/release-r2-immutable-put.sh")
	verify := strings.Index(text, "public_bytes")
	if prepare < 0 || deposit <= prepare || verify <= deposit || !strings.Contains(text, "for name in distribution.v1.json distribution.v1.json.sig") {
		t.Fatal("both signed document objects must be constructed, deposited and publicly verified in order")
	}
	for _, name := range []string{"update-cask", "notify-website", "activate-updater", "publish-github-release"} {
		if !workflow.ancestors(name)["publish-immutable"] {
			t.Errorf("%s bypasses the completed immutable document gate", name)
		}
	}
	notice := workflow.Jobs["notify-website"].text()
	for _, required := range []string{"signed-distribution/distribution.v1.json", `"distribution_sha256"`, "--distribution-sha256", "install -y minisign", `"publication_channels": plan["channels"]`, "--input"} {
		if !strings.Contains(notice, required) {
			t.Errorf("website confirmation lacks %s", required)
		}
	}
	delivery := parseLane(t, ".github/workflows/release-linux-delivery.yml")
	if !slices.Contains(delivery.Jobs["publish-nix-cache"].artifacts("upload-artifact"), "nix-cache-publication") ||
		!slices.Contains(workflow.Jobs["publish-linux-repositories"].artifacts("upload-artifact"), "linux-repository-publication") {
		t.Fatal("document facts must select actual current-run delivery receipts")
	}
	mirror := workflow.Jobs["publish-github-release"].text()
	if !strings.Contains(mirror, "distribution.v1.json") || !strings.Contains(mirror, "distribution.v1.json.sig") {
		t.Fatal("the trailing mirror must retain both signed document objects")
	}
}

func TestReleasePublicationBoundaryExcludesCandidateBytes(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	output := filepath.Join(dir, "output")
	contractcheck.FailErr(t, "create artifact input", os.Mkdir(input, 0o755))
	version := "1.0.0-rc.1"
	catalogPath := filepath.Join(dir, "candidate-catalog.json")
	contractcheck.FailErr(t, "write candidate catalog", os.WriteFile(catalogPath, []byte(`{"schema_version":2,"platforms":[
        {"updater_key":"darwin-aarch64","publication":"public","package_extensions":["dmg"],"updater_extension":"app.tar.gz"},
        {"updater_key":"linux-aarch64","publication":"candidate","package_extensions":["deb"],"updater_extension":null}]}`), 0o644))
	publicStem := "painted-wolf-code_v" + version + "_darwin-aarch64"
	contractcheck.FailErr(t, "create audit input", os.Mkdir(filepath.Join(input, "opengrep"), 0o755))
	audit := filepath.Join(input, "opengrep", publicStem+".json")
	contractcheck.FailErr(t, "write public engine audit", os.WriteFile(audit, []byte(`{"mode":"reviewed-pin-only"}`), 0o644))
	contractcheck.FailErr(t, "write candidate engine audit", os.WriteFile(filepath.Join(input, "opengrep", "painted-wolf-code_v"+version+"_linux-aarch64.json"), []byte(`{"mode":"reviewed-pin-only"}`), 0o644))
	for _, suffix := range []string{"dmg", "dmg.sha256", "app.tar.gz", "app.tar.gz.sig"} {
		contractcheck.FailErr(t, "write public artifact", os.WriteFile(filepath.Join(input, publicStem+"."+suffix), []byte(suffix), 0o644))
	}
	candidate := "painted-wolf-code_v" + version + "_linux-aarch64.deb"
	contractcheck.FailErr(t, "write candidate artifact", os.WriteFile(filepath.Join(input, candidate), []byte("candidate"), 0o644))
	cmd := exec.Command(
		"python3", filepath.Join(root, "scripts", "release-collect-public-artifacts.py"),
		"--catalog", catalogPath,
		"--input", input, "--output", output, "--version", version,
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("collect public artifacts: %v\n%s", err, combined)
	}
	entries, err := os.ReadDir(output)
	contractcheck.FailErr(t, "read public artifact output", err)
	if len(entries) != 5 {
		t.Fatalf("public artifact count = %d, want 5", len(entries))
	}
	if _, err := os.Stat(filepath.Join(output, candidate)); !os.IsNotExist(err) {
		t.Fatalf("candidate artifact crossed publication boundary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "painted-wolf-code_v"+version+"_linux-aarch64.opengrep.json")); !os.IsNotExist(err) {
		t.Fatal("candidate audit crossed publication boundary")
	}
	contractcheck.FailErr(t, "remove required engine audit", os.Remove(audit))
	missingOutput := filepath.Join(dir, "missing-audit")
	missing := exec.Command("python3", filepath.Join(root, "scripts", "release-collect-public-artifacts.py"), "--catalog", catalogPath, "--input", input, "--output", missingOutput, "--version", version)
	if combined, err := missing.CombinedOutput(); err == nil {
		t.Fatalf("missing engine audit accepted: %s", combined)
	}
}

func TestReleasePublicationCollectsSharedInstallerUpdaterOnce(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	for _, extension := range []string{"exe"} {
		t.Run(extension, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "input"), filepath.Join(dir, "output")
			contractcheck.FailErr(t, "create audit input", os.MkdirAll(filepath.Join(input, "opengrep"), 0o755))
			stem := "painted-wolf-code_v1.0.0_linux-x86_64"
			for _, suffix := range []string{extension, extension + ".sha256", extension + ".sig"} {
				contractcheck.FailErr(t, "write artifact", os.WriteFile(filepath.Join(input, stem+"."+suffix), []byte(suffix), 0o644))
			}
			contractcheck.FailErr(t, "write audit", os.WriteFile(filepath.Join(input, "opengrep", stem+".json"), []byte(`{}`), 0o644))
			catalog := filepath.Join(dir, "catalog.json")
			row := map[string]any{"updater_key": "linux-x86_64", "publication": "public", "package_extensions": []string{extension}, "updater_extension": extension}
			writeCatalog := func(rows []map[string]any) {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"schema_version": 2, "platforms": rows})
				contractcheck.FailErr(t, "encode catalog", err)
				contractcheck.FailErr(t, "write catalog", os.WriteFile(catalog, raw, 0o644))
			}
			writeCatalog([]map[string]any{row})
			run := func(destination string, wantSuccess bool) {
				t.Helper()
				cmd := exec.Command("python3", filepath.Join(root, "scripts", "release-collect-public-artifacts.py"), "--catalog", catalog, "--input", input, "--output", destination, "--version", "1.0.0")
				combined, err := cmd.CombinedOutput()
				if (err == nil) != wantSuccess {
					t.Fatalf("collector success=%v want %v: %v\n%s", err == nil, wantSuccess, err, combined)
				}
			}
			run(output, true)
			entries, err := os.ReadDir(output)
			contractcheck.FailErr(t, "read shared artifact set", err)
			if len(entries) != 4 {
				t.Fatalf("shared artifact count=%d, want 4", len(entries))
			}
			writeCatalog([]map[string]any{row, row})
			run(filepath.Join(dir, "repeated"), false)
			writeCatalog([]map[string]any{row})
			contractcheck.FailErr(t, "remove signature", os.Remove(filepath.Join(input, stem+"."+extension+".sig")))
			run(filepath.Join(dir, "unsigned"), false)
		})
	}
}

func TestReleasePublicationCollectsEveryCurrentPublicPackage(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	dir := t.TempDir()
	input, output := filepath.Join(dir, "input"), filepath.Join(dir, "output")
	contractcheck.FailErr(t, "create audit directory", os.MkdirAll(filepath.Join(input, "opengrep"), 0o755))
	var catalog releasePlatformCatalog
	contractcheck.FailErr(t, "read current public catalog", json.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, "packaging/release-platforms.json")), &catalog))
	expected := map[string][]byte{}
	for _, platform := range catalog.Platforms {
		if platform.Publication != "public" {
			continue
		}
		stem := "painted-wolf-code_v1.2.3_" + platform.UpdaterKey
		names := map[string]bool{}
		for _, extension := range platform.PackageExts {
			names[stem+"."+extension], names[stem+"."+extension+".sha256"] = true, true
			if platform.GOOS == "linux" && extension != "rpm" {
				names[stem+"."+extension+".sig"] = true
			}
		}
		if platform.UpdaterExt != nil {
			names[stem+"."+*platform.UpdaterExt], names[stem+"."+*platform.UpdaterExt+".sig"] = true, true
		}
		for name := range names {
			raw := []byte(name)
			contractcheck.FailErr(t, "write public package object", os.WriteFile(filepath.Join(input, name), raw, 0o644))
			expected[name] = raw
		}
		audit := []byte(`{"mode":"reviewed-pin-only"}`)
		contractcheck.FailErr(t, "write public audit", os.WriteFile(filepath.Join(input, "opengrep", stem+".json"), audit, 0o644))
		expected[stem+".opengrep.json"] = audit
	}
	if len(expected) == 0 {
		t.Fatal("current public catalog produced no release objects to verify")
	}
	command := func(destination string) ([]byte, error) {
		return exec.Command("python3", filepath.Join(root, "scripts/release-collect-public-artifacts.py"), "--catalog", filepath.Join(root, "packaging/release-platforms.json"), "--input", input, "--output", destination, "--version", "1.2.3").CombinedOutput()
	}
	if raw, err := command(output); err != nil {
		t.Fatalf("collect current public package set: %v\n%s", err, raw)
	}
	entries, err := os.ReadDir(output)
	contractcheck.FailErr(t, "read complete public artifact set", err)
	if len(entries) != len(expected) {
		t.Fatalf("public artifact count=%d want %d", len(entries), len(expected))
	}
	for name, want := range expected {
		actual, err := os.ReadFile(filepath.Join(output, name))
		contractcheck.FailErr(t, "read collected package bytes", err)
		if string(actual) != string(want) {
			t.Fatalf("public object %s changed bytes", name)
		}
	}
	contractcheck.FailErr(t, "remove required Linux signature", os.Remove(filepath.Join(input, "painted-wolf-code_v1.2.3_linux-aarch64.deb.sig")))
	if raw, err := command(filepath.Join(dir, "missing-linux-signature")); err == nil {
		t.Fatalf("unsigned public Linux package accepted: %s", raw)
	}
}

func TestReleaseChannelPlanFeedsEveryPublicationSurface(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	for _, name := range []string{"linux-repositories", "linux-pacman-repositories", "publish-artifacts", "publish-linux-repositories", "publish-immutable", "update-cask", "notify-website", "activate-updater"} {
		job := workflow.Jobs[name]
		if !workflow.ancestors(name)["select-release-channels"] || !slices.Contains(job.artifacts("download-artifact"), "release-channel-plan") ||
			!strings.Contains(job.text(), "release-plan/channel-plan.json") {
			t.Errorf("%s does not consume the one retained release channel plan", name)
		}
	}
	deposit := workflow.Jobs["publish-artifacts"].text()
	if !strings.Contains(deposit, "release-metadata/${VERSION}/channel-plan.json") || !strings.Contains(deposit, "channels check") {
		t.Fatal("the original plan must be re-admitted and retained immutably before channel effects")
	}
	activation := workflow.Jobs["activate-updater"].text()
	if !strings.Contains(activation, ".channels[]") || !strings.Contains(activation, "--advance-if-newer") {
		t.Fatal("updater activation must consume the shared plan and keep its monotonic Preview guard")
	}
}

func TestReleaseMirrorRefusesWithdrawnIntentBeforeProviderAccess(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	job := parseLane(t, ".github/workflows/release.yml").Jobs["publish-github-release"]
	var body string
	var mirror laneStep
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "gh release create") {
			body, mirror = step.Run, step
		}
	}
	if body == "" || job.Environment != "release-publication" {
		t.Fatal("mirror must use its shared protected publication owner")
	}
	for _, name := range []string{"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_TOKEN", "R2_BUCKET"} {
		if mirror.Env[name] != "${{ secrets."+name+" }}" {
			t.Fatalf("mirror ledger read requires the existing protected %s credential", name)
		}
	}
	work := t.TempDir()
	setup := `import json,pathlib,sys
from scripts.verification_tests.distribution.release_provider_fixtures import Providers
from scripts.release_distribution_contract.channels import select
from update_keys import load_registry
root,source=map(pathlib.Path,sys.argv[1:])
key=root/'unused-disposable.key';key.write_bytes(b'fixture-only')
p=Providers(root,source,load_registry(),key)
plan=select('stable','1.2.3',1,'a'*40,read=lambda key:{'version':'1.2.2'} if key.startswith('updates/') else None)
(p.controller/'release-plan').mkdir()
(p.controller/'release-plan/channel-plan.json').write_text(json.dumps(plan))
for channel,value in plan['controls'].items():p.put('updates/'+channel+'/key-1/latest.json',json.dumps(value).encode())
p.put('release-metadata/1.2.3/withdrawn.json',b'{"version":"1.2.3","withdrawn":true}')
(p.controller/'CHANGELOG.md').write_text('## [1.2.3]\nFixture notes\n')
gh=p.bin/'gh'
gh.write_text('#!'+sys.executable+'\nimport json,pathlib,sys\np=pathlib.Path('+repr(str(root/'github-effects'))+')\nwith p.open("a") as out:out.write(json.dumps(sys.argv[1:])+"\\n")\nraise SystemExit(1 if "view" in sys.argv else 0)\n')
gh.chmod(0o700)
print(json.dumps(p.environment))`
	command := exec.Command("python3", "-c", setup, work, root)
	command.Dir = root
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "scripts"))
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare isolated mirror providers: %v\n%s", err, raw)
	}
	var environment map[string]string
	if err := json.Unmarshal(raw, &environment); err != nil {
		t.Fatalf("decode isolated provider environment: %v\n%s", err, raw)
	}
	environment["VERSION"], environment["PRERELEASE"], environment["RUNNER_TEMP"] = "1.2.3", "false", work
	run := func() ([]byte, error) {
		command := exec.Command("bash", "-c", body)
		command.Dir = filepath.Join(work, "controller")
		for name, value := range environment {
			command.Env = append(command.Env, name+"="+value)
		}
		return command.CombinedOutput()
	}
	output, err := run()
	if err == nil || !strings.Contains(string(output), "withdrawn release cannot be published") {
		t.Fatalf("withdrawn mirror rerun must refuse at the common ledger: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(work, "github-effects")); !os.IsNotExist(err) {
		t.Fatal("withdrawn mirror rerun reached the GitHub provider")
	}
	contractcheck.FailErr(t, "remove withdrawal metadata for mirror positive control", os.Remove(filepath.Join(work, "objects/release-metadata/1.2.3/withdrawn.json")))
	if output, err := run(); err != nil {
		t.Fatalf("unwithdrawn mirror positive control: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(filepath.Join(work, "github-effects"))
	if err != nil || !strings.Contains(string(calls), `"create"`) {
		t.Fatalf("positive control must reach the existing mirror mutation: %v\n%s", err, calls)
	}
}
