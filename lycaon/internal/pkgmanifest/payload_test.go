package pkgmanifest_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func payload(m map[string]any) []map[string]any { return m["payload"].([]map[string]any) }

func TestPayloadManifestRejectsIncompleteOrUnsafeGenerationInventory(t *testing.T) {
	cases := map[string]func(map[string]any){
		"absent inventory":        func(m map[string]any) { delete(m, "payload") },
		"empty inventory":         func(m map[string]any) { m["payload"] = []map[string]any{} },
		"missing explicit field":  func(m map[string]any) { delete(payload(m)[1], "target") },
		"foreign field":           func(m map[string]any) { payload(m)[1]["authority"] = true },
		"escaping path":           func(m map[string]any) { payload(m)[1]["path"] = "../pw" },
		"absolute path":           func(m map[string]any) { payload(m)[1]["path"] = "/bin/pw" },
		"self reference":          func(m map[string]any) { payload(m)[1]["path"] = "manifest.json" },
		"writable image":          func(m map[string]any) { payload(m)[1]["mode"] = 0777 },
		"setgid image":            func(m map[string]any) { payload(m)[1]["mode"] = 02755 },
		"missing physical parent": func(m map[string]any) { m["payload"] = payload(m)[1:] },
		"directory as image":      func(m map[string]any) { payload(m)[1]["kind"] = "directory" },
		"image content differs":   func(m map[string]any) { payload(m)[1]["size"] = 0 },
		"duplicate path":          func(m map[string]any) { m["payload"] = append(payload(m), payload(m)[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			file, _ := fixture(t, mutate)
			if _, err := pkgmanifest.Load(file); err == nil {
				t.Fatal("accepted unsafe payload inventory")
			}
		})
	}
}

func TestPayloadLinksStayInsideOneCompleteGeneration(t *testing.T) {
	for _, target := range []string{"../bin/pw", "../bin", ".."} {
		t.Run(target, func(t *testing.T) {
			file, _ := fixture(t, func(m map[string]any) {
				m["payload"] = append(payload(m), map[string]any{"path": "bin/alias", "kind": "symlink", "mode": 0777, "sha256": nil, "size": nil, "target": target})
			})
			if _, err := pkgmanifest.Load(file); err != nil {
				testutil.FailErr(t, "pkgmanifest.Load failed", err)
			}
		})
	}
	for _, target := range []string{"../../escape", "/nix/store/arbitrary/bin/pw", "missing", "alias"} {
		t.Run(target, func(t *testing.T) {
			file, _ := fixture(t, func(m map[string]any) {
				m["payload"] = append(payload(m), map[string]any{"path": "bin/alias", "kind": "symlink", "mode": 0777, "sha256": nil, "size": nil, "target": target})
			})
			if _, err := pkgmanifest.Load(file); err == nil {
				t.Fatal("accepted escaping, dangling or cyclic link")
			}
		})
	}
}

func TestRealPackagingScriptManifestAcceptedByReaders(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	testutil.FailErr(t, "resolve repo root", err)
	rawCatalog, err := os.ReadFile(filepath.Join(repoRoot, "packaging", "linux", "payload.json"))
	testutil.FailErr(t, "read payload.json", err)
	var catalog struct {
		Helpers         []string `json:"helpers"`
		EntryPoints     []string `json:"entry_points"`
		SetuidBootstrap *struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"setuid_bootstrap"`
	}
	testutil.FailErr(t, "decode payload.json", json.Unmarshal(rawCatalog, &catalog))

	work := t.TempDir()
	fakeRepo := filepath.Join(work, "repo")
	staged := filepath.Join(work, "staged")
	installDir := filepath.Join(staged, "opt", "painted-wolf-code")
	target := "x86_64-unknown-linux-gnu"
	tauri := filepath.Join(fakeRepo, "lycaon-den", "src-tauri")

	writeFile := func(path string, content []byte, mode os.FileMode) {
		testutil.FailErr(t, "mkdir "+path, os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "chmod dir "+path, os.Chmod(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+path, os.WriteFile(path, content, mode))
		testutil.FailErr(t, "chmod "+path, os.Chmod(path, mode))
	}

	writeFile(filepath.Join(fakeRepo, "VERSION"), []byte("1.2.3\n"), 0o644)
	writeFile(filepath.Join(tauri, "THIRD-PARTY-NOTICES.md"), []byte("notices\n"), 0o644)
	writeFile(filepath.Join(tauri, "engine-root", "schemas", "oar", "oar.schema.json"), []byte("{}\n"), 0o644)
	writeFile(filepath.Join(tauri, "target", target, "release", "painted-wolf-code"), []byte("#!/bin/sh\n"), 0o755)
	writeFile(filepath.Join(installDir, "release-version"), []byte("1.2.3\n"), 0o644)
	writeFile(filepath.Join(installDir, "THIRD-PARTY-NOTICES.md"), []byte("notices\n"), 0o644)
	writeFile(filepath.Join(installDir, "engine-root", "schemas", "oar", "oar.schema.json"), []byte("{}\n"), 0o644)
	writeFile(filepath.Join(installDir, "bin", "painted-wolf-code"), []byte("#!/bin/sh\n"), 0o755)

	for _, helper := range catalog.Helpers {
		writeFile(filepath.Join(tauri, "binaries", helper+"-"+target), []byte("#!/bin/sh\n"), 0o755)
		writeFile(filepath.Join(installDir, "bin", helper), []byte("#!/bin/sh\n"), 0o755)
	}
	for _, entry := range catalog.EntryPoints {
		writeFile(filepath.Join(fakeRepo, "packaging", "linux", "bin", entry), []byte("#!/bin/sh\n"), 0o755)
		writeFile(filepath.Join(staged, "usr", "bin", entry), []byte("#!/bin/sh\n"), 0o755)
	}
	if catalog.SetuidBootstrap != nil {
		writeFile(filepath.Join(tauri, "binaries", catalog.SetuidBootstrap.Name+"-"+target), []byte("#!/bin/sh\n"), 0o755)
		writeFile(filepath.Join(fakeRepo, "packaging", "linux", "protected-launch.json"), []byte("{}\n"), 0o644)
		writeFile(filepath.Join(installDir, "protected-launch.json"), []byte("{}\n"), 0o644)
		writeFile(filepath.Join(staged, strings.TrimPrefix(catalog.SetuidBootstrap.Path, "/")), []byte("#!/bin/sh\n"), 0o755)
	}
	for _, dir := range []string{
		installDir,
		filepath.Join(installDir, "bin"),
		filepath.Join(installDir, "engine-root"),
		filepath.Join(installDir, "engine-root", "schemas"),
		filepath.Join(installDir, "engine-root", "schemas", "oar"),
		filepath.Join(tauri, "engine-root"),
		filepath.Join(tauri, "engine-root", "schemas"),
		filepath.Join(tauri, "engine-root", "schemas", "oar"),
	} {
		testutil.FailErr(t, "chmod dir "+dir, os.Chmod(dir, 0o755))
	}

	manifestPath := filepath.Join(installDir, "manifest.json")
	script := filepath.Join(repoRoot, "scripts", "linux-package-manifest.py")
	runScript := func(args ...string) {
		cmd := exec.Command("python3", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(repoRoot, "scripts"))
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("linux-package-manifest.py %v failed: %v\n%s", args, runErr, out)
		}
	}
	runScript("generate", "--root", fakeRepo, "--target", target, "--version", "1.2.3", "--output", manifestPath)
	runScript("seal-tree", "--tree", staged)
	runScript("verify-tree", "--tree", staged, "--version", "1.2.3", "--architecture", "x86_64")

	m, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "pkgmanifest.Load generated manifest", err)
	if m.FormatVersion != pkgmanifest.FormatVersion || len(m.Payload) == 0 {
		t.Fatalf("loaded manifest = %+v", m)
	}

	raw, err := os.ReadFile(manifestPath)
	testutil.FailErr(t, "read manifest", err)
	relocated := strings.ReplaceAll(string(raw), `"/opt/painted-wolf-code`, `"`+installDir)
	testutil.FailErr(t, "write relocated manifest", os.WriteFile(manifestPath, []byte(relocated), 0o644))
	localManifest, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "pkgmanifest.Load relocated manifest", err)
	retained, err := os.OpenRoot(installDir)
	testutil.FailErr(t, "os.OpenRoot", err)
	defer retained.Close()
	testutil.FailErr(t, "VerifyPayload", localManifest.VerifyPayload(retained))
	testutil.FailErr(t, "Verify shell", localManifest.Verify(filepath.Join(installDir, "bin", "painted-wolf-code")))
}
