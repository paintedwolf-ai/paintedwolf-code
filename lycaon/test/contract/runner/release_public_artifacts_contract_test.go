package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
