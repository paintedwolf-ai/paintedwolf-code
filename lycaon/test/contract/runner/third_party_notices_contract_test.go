package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestLicensesNoticesTaskExists(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	taskfile := contractcheck.ReadRepoFile(t, root, "Taskfile.yml")
	if !strings.Contains(taskfile, "licenses:notices:") {
		t.Fatal("Taskfile.yml must define licenses:notices")
	}
	script := filepath.Join(root, "scripts", "licenses-notices.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("missing %s: %v", script, err)
	}
}

func TestBundledBinariesCatalogParsesAndHasRequiredFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "licensing", "bundled-binaries.yaml"))
	contractcheck.FailErr(t, "read bundled binaries catalog", err)
	var catalog struct {
		Binaries []struct {
			Name            string `yaml:"name"`
			Version         string `yaml:"version"`
			License         string `yaml:"license"`
			SourceURL       string `yaml:"source_url"`
			SourcePath      string `yaml:"source_path"`
			LicenseTextFile string `yaml:"license_text_file"`
			Artifact        string `yaml:"artifact"`
		} `yaml:"binaries"`
	}
	contractcheck.FailErr(t, "parse bundled binaries catalog", yaml.Unmarshal(data, &catalog))
	seen := make(map[string]bool)
	for _, binary := range catalog.Binaries {
		if binary.Name == "" || seen[binary.Name] {
			t.Fatalf("invalid binary attribution: %+v", binary)
		}
		seen[binary.Name] = true
		if binary.Name == "opengrep" || binary.Artifact != "" {
			if binary.Name != "opengrep" || binary.Artifact != "opengrep" || binary.Version != "" || binary.License != "" || binary.SourcePath != "" || binary.SourceURL != "" || binary.LicenseTextFile != "" {
				t.Fatalf("artifact attribution must derive from selected release: %+v", binary)
			}
			continue
		}
		if binary.Version == "" || binary.License == "" || (binary.SourceURL == "") == (binary.SourcePath == "") {
			t.Fatalf("invalid binary attribution: %+v", binary)
		}
		if binary.SourcePath != "" && (!filepath.IsLocal(binary.SourcePath) || filepath.ToSlash(filepath.Clean(binary.SourcePath)) != binary.SourcePath) {
			t.Fatalf("source path escapes application resources: %q", binary.SourcePath)
		}
		if binary.LicenseTextFile != "" {
			_, err := os.Stat(filepath.Join(root, "licensing", binary.LicenseTextFile))
			contractcheck.FailErr(t, "read license for "+binary.Name, err)
		}
	}
	if !seen["opengrep"] || !seen["chrome-headless-shell"] {
		t.Fatal("bundled scanner or browser attribution missing")
	}
}

func TestLicensesNoticesFailClosedOnUnknown(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	tmp := t.TempDir()
	goCSV := filepath.Join(tmp, "go.csv")
	// Minimal valid row + an Unknown row the assemble step must reject.
	contractcheck.FailErr(t, "write go.csv", os.WriteFile(goCSV, []byte(
		"github.com/example/ok,https://example.com,MIT\n"+
			"github.com/example/unlicensed-fixture,Unknown,Unknown\n",
	), 0o644))
	npmMD := filepath.Join(tmp, "npm.md")
	contractcheck.FailErr(t, "write npm.md", os.WriteFile(npmMD, []byte("## Den npm packages\n\nok\n"), 0o644))
	crates := filepath.Join(tmp, "crates.json")
	contractcheck.FailErr(t, "write crates.json", os.WriteFile(crates, []byte(
		`{"licenses":[{"id":"MIT","text":"MIT","used_by":[{"crate":{"name":"x","version":"1"}}]}]}`,
	), 0o644))
	saveDir := filepath.Join(tmp, "go-save")
	contractcheck.FailErr(t, "mkdir go-save", os.MkdirAll(saveDir, 0o755))
	out := filepath.Join(tmp, "out.md")

	cmd := exec.Command(
		"bun", filepath.Join(root, "scripts", "licenses-assemble.ts"),
		"--go-csv", goCSV,
		"--go-save", saveDir,
		"--npm-md", npmMD,
		"--crates-json", crates,
		"--document-crates-json", crates,
		"--decide-crates-json", crates,
		"--opengrep-artifact", filepath.Join(tmp, "unused artifact"),
		"--out", out,
	)
	cmd.Dir = root
	outBytes, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected assemble to fail on Unknown; output:\n%s", outBytes)
	}
	if !strings.Contains(string(outBytes), "unclassifiable") {
		t.Fatalf("assemble should cite unclassifiable; got:\n%s", outBytes)
	}
}

func TestAboutPanelHasThirdPartySoftwareRow(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	about := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/components/settings/system/AboutSettingsPanel.tsx")
	for _, want := range []string{
		"Third-party software",
		"about-third-party",
		"openThirdPartyNotices",
	} {
		if !strings.Contains(about, want) {
			t.Fatalf("AboutSettingsPanel missing %q", want)
		}
	}
}

func TestNoticesGitignoredAndWorkflowsWired(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	gitignore := contractcheck.ReadRepoFile(t, root, ".gitignore")
	if !strings.Contains(gitignore, "THIRD-PARTY-NOTICES.md") {
		t.Fatal(".gitignore must ignore THIRD-PARTY-NOTICES.md")
	}
	setup := contractcheck.ReadRepoFile(t, root, ".github/actions/setup-verification/action.yml")
	if !strings.Contains(setup, "run: ./task licenses:notices") {
		t.Fatal("hosted verification setup must generate third-party notices")
	}
	tauri := contractcheck.ReadRepoFile(t, root, "lycaon-den/src-tauri/tauri.conf.json")
	if !strings.Contains(tauri, "THIRD-PARTY-NOTICES.md") {
		t.Fatal("tauri.conf.json must map THIRD-PARTY-NOTICES.md into bundle.resources")
	}
}

func TestLicensesNoticesReadSelectedArtifact(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var manifest bundled.Manifest
	contractcheck.FailErr(t, "parse released scanner selection", yaml.Unmarshal(
		[]byte(contractcheck.ReadRepoFile(t, root, "lycaon/config/runtime/scanners/bundled-manifest.yaml")), &manifest,
	))
	for _, test := range []struct {
		name, version, notice, wantError string
	}{
		{name: "selected release", version: manifest.OpenGrep.Version, notice: "# Released component notices\n\nUnique artifact attribution.\n\n## Dependency\n\n```\n# Literal license heading\nFull dependency license.\nFinal clause.\n```\n"},
		{name: "wrong version", version: "1.0.0", notice: "release attribution", wantError: "differs from selected release"},
		{name: "empty notice", version: manifest.OpenGrep.Version, notice: " \n", wantError: "artifact notice is empty"},
		{name: "missing notice", version: manifest.OpenGrep.Version, wantError: "NOTICES-opengrep.md"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			artifact := filepath.Join(tmp, "released artifact")
			scannerScriptFile(t, filepath.Join(artifact, "LICENSE"), "Released engine license.\nFirst clause.\nFinal clause.\n")
			scannerScriptFile(t, filepath.Join(artifact, "provenance.json"), `{"version":"`+test.version+`"}`)
			if test.notice != "" {
				scannerScriptFile(t, filepath.Join(artifact, "NOTICES-opengrep.md"), test.notice)
			}
			scannerScriptFile(t, filepath.Join(tmp, "go.csv"), "github.com/example/ok,https://example.com,MIT\n")
			scannerScriptFile(t, filepath.Join(tmp, "npm.md"), "## Den npm packages\n\nFixture attribution.\n")
			scannerScriptFile(t, filepath.Join(tmp, "crates.json"), `{"licenses":[{"id":"MIT","text":"MIT","used_by":[{"crate":{"name":"fixture","version":"1"}}]}]}`)
			outputPath := filepath.Join(tmp, "out.md")
			cmd := exec.CommandContext(t.Context(), "bun", filepath.Join(root, "scripts/licenses-assemble.ts"),
				"--go-csv", filepath.Join(tmp, "go.csv"), "--go-save", filepath.Join(tmp, "go-save"),
				"--npm-md", filepath.Join(tmp, "npm.md"), "--crates-json", filepath.Join(tmp, "crates.json"),
				"--document-crates-json", filepath.Join(tmp, "crates.json"),
				"--decide-crates-json", filepath.Join(tmp, "crates.json"),
				"--opengrep-artifact", artifact, "--out", outputPath)
			output, err := cmd.CombinedOutput()
			if test.wantError != "" {
				if err == nil || !strings.Contains(string(output), test.wantError) {
					t.Fatalf("expected %q; error=%v output=%s", test.wantError, err, output)
				}
				if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
					t.Fatalf("invalid artifact produced notices: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("assemble released notices: %v\n%s", err, output)
			}
			assembled, err := os.ReadFile(outputPath)
			contractcheck.FailErr(t, "read assembled artifact notices", err)
			for _, want := range []string{"#### Released component notices\n\nUnique artifact attribution.", "Released engine license.\nFirst clause.\nFinal clause.", "##### Dependency\n\n```\n# Literal license heading\nFull dependency license.\nFinal clause.\n```",
				"### opengrep " + manifest.OpenGrep.Version, "engine-root/bundled/opengrep-" + manifest.OpenGrep.Version + "/opengrep-source.tar.gz"} {
				if !strings.Contains(string(assembled), want) {
					t.Fatalf("assembled notices lack selected artifact attribution %q", want)
				}
			}
		})
	}
}
