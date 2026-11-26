package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	ImageID       string `json:"image_id"`
	Target        string `json:"target"`
	Interpreter   struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		WasmPath   string `json:"wasm_path"`
		WasmSHA256 string `json:"wasm_sha256"`
		StdlibPath string `json:"stdlib_path"`
	} `json:"interpreter"`
	PackagesRoot  string `json:"packages_root"`
	Distributions []struct {
		Name           string   `json:"name"`
		NormalizedName string   `json:"normalized_name"`
		Version        string   `json:"version"`
		Public         bool     `json:"public"`
		ConsoleScripts []string `json:"console_scripts"`
	} `json:"distributions"`
	WasmImports []struct {
		Module string `json:"module"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
	} `json:"wasm_imports"`
}

func runPython(ctx context.Context, r wazero.Runtime, compiled wazero.CompiledModule, libDir, packagesDir, scratchDir string, stdin []byte, args ...string) (string, string, int, error) {
	var stdout, stderr bytes.Buffer
	mounts := wazero.NewFSConfig().
		WithReadOnlyDirMount(libDir, "/usr/local/lib").
		WithReadOnlyDirMount(packagesDir, "/packages").
		WithDirMount(scratchDir, "/tmp")

	cfg := wazero.NewModuleConfig().
		WithFSConfig(mounts).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithArgs(append([]string{"python3"}, args...)...)
	if stdin != nil {
		cfg = cfg.WithStdin(bytes.NewReader(stdin))
	}

	_, err := r.InstantiateModule(ctx, compiled, cfg)
	exitCode := 0
	if err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) {
			exitCode = int(exitErr.ExitCode())
			err = nil
		}
	}
	return stdout.String(), stderr.String(), exitCode, err
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <image-dir> <smoke_cases.py>\n", os.Args[0])
		os.Exit(2)
	}
	imageDir := os.Args[1]
	smokeScriptPath := os.Args[2]

	manifestBytes, err := os.ReadFile(filepath.Join(imageDir, "image-manifest.json"))
	if err != nil {
		panic(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		panic(err)
	}
	if manifest.SchemaVersion != "portable-python.image-manifest/v1" {
		panic("unexpected schema_version: " + manifest.SchemaVersion)
	}
	fmt.Printf("manifest: schema=%s image_id=%s target=%s interp=%s %s dists=%d\n",
		manifest.SchemaVersion, manifest.ImageID, manifest.Target,
		manifest.Interpreter.Name, manifest.Interpreter.Version, len(manifest.Distributions))

	wasmPath := filepath.Join(imageDir, manifest.Interpreter.WasmPath)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(wasmBytes)
	actualWasmHash := hex.EncodeToString(sum[:])
	if actualWasmHash != manifest.Interpreter.WasmSHA256 {
		panic(fmt.Sprintf("wasm sha256 mismatch: manifest=%s actual=%s", manifest.Interpreter.WasmSHA256, actualWasmHash))
	}
	fmt.Printf("wasm_sha256: VERIFIED (%s)\n", actualWasmHash)

	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		panic(err)
	}

	// Verify wazero-inspected imports match manifest wasm_imports
	gotImports := make(map[string]bool)
	for _, imp := range compiled.ImportedFunctions() {
		mod, name, _ := imp.Import()
		gotImports[mod+"."+name] = true
		if mod != "wasi_snapshot_preview1" {
			panic(fmt.Sprintf("unexpected non-WASI import: %s.%s", mod, name))
		}
	}
	manifestImports := make(map[string]bool)
	for _, imp := range manifest.WasmImports {
		if imp.Kind == "func" {
			manifestImports[imp.Module+"."+imp.Name] = true
		}
	}
	if len(gotImports) != len(manifestImports) {
		panic(fmt.Sprintf("wasm import count mismatch: wazero=%d manifest=%d", len(gotImports), len(manifestImports)))
	}
	for k := range gotImports {
		if !manifestImports[k] {
			panic("wazero import missing from manifest: " + k)
		}
	}
	fmt.Printf("wasm_imports: VERIFIED (%d wasi_snapshot_preview1 functions)\n", len(gotImports))

	libDir := filepath.Join(imageDir, "lib")
	packagesDir := filepath.Join(imageDir, manifest.PackagesRoot)
	scratchDir, err := os.MkdirTemp("", "pp-smoke-scratch-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(scratchDir)

	// 1. Run comprehensive guest smoke_cases.py
	scriptBytes, err := os.ReadFile(smokeScriptPath)
	if err != nil {
		panic(err)
	}
	fmt.Println("\n=== Guest Library & Stdlib Smoke Suite ===")
	stdout, stderr, code, err := runPython(ctx, r, compiled, libDir, packagesDir, scratchDir, nil, "-I", "-B", "-c", string(scriptBytes))
	fmt.Print(stdout)
	if stderr != "" {
		fmt.Fprintf(os.Stderr, "[stderr]\n%s\n", stderr)
	}
	if err != nil || code != 0 {
		fmt.Fprintf(os.Stderr, "smoke_cases.py failed: code=%d err=%v\n", code, err)
		os.Exit(1)
	}

	// 2. Run markdown-it CLI console script entry point on a file
	fmt.Println("\n=== CLI Smoke: markdown-it (file -> HTML) ===")
	mdFile := filepath.Join(scratchDir, "smoke.md")
	if err := os.WriteFile(mdFile, []byte("# Portable Python\n\n```python\nprint('hello')\n```\n"), 0o644); err != nil {
		panic(err)
	}
	mdEntry := "import sys; sys.path.insert(0, '/packages'); from importlib.metadata import entry_points; ep = next(iter(entry_points(group='console_scripts', name='markdown-it'))); sys.argv = ['markdown-it', '/tmp/smoke.md']; raise SystemExit(ep.load()())"
	stdout, stderr, code, err = runPython(ctx, r, compiled, libDir, packagesDir, scratchDir, nil, "-I", "-B", "-c", mdEntry)
	if err != nil || code != 0 {
		fmt.Fprintf(os.Stderr, "markdown-it CLI failed: code=%d err=%v stderr=%s\n", code, err, stderr)
		os.Exit(1)
	}
	if !strings.Contains(stdout, "<h1>Portable Python</h1>") || !strings.Contains(stdout, "<code class=\"language-python\">") {
		fmt.Fprintf(os.Stderr, "markdown-it CLI unexpected output: %q\n", stdout)
		os.Exit(1)
	}
	fmt.Printf("markdown_it_cli PASS (%d bytes HTML)\n", len(stdout))

	// 3. Run python -m pytest directly via fresh wazero guest
	fmt.Println("\n=== CLI Smoke: python -m pytest ===")
	testFile := filepath.Join(scratchDir, "test_cli_smoke.py")
	if err := os.WriteFile(testFile, []byte("import pytest\n\n@pytest.mark.parametrize('a,b,c', [(1,2,3),(4,5,9)])\ndef test_add(a,b,c):\n    assert a + b == c\n\ndef test_tmp(tmp_path):\n    p = tmp_path / 'x.txt'\n    p.write_text('ok')\n    assert p.read_text() == 'ok'\n"), 0o644); err != nil {
		panic(err)
	}
	pytestBootstrap := "import os, sys; sys.path.insert(0, '/packages'); os.chdir('/tmp'); import runpy; sys.argv = ['pytest', '-q', '--capture=sys', '-p', 'no:cacheprovider', '-p', 'no:faulthandler', '-o', 'log_file=/tmp/pytest-cli.log', '--rootdir=/tmp', '/tmp/test_cli_smoke.py']; runpy.run_module('pytest', run_name='__main__')"
	stdout, stderr, code, err = runPython(ctx, r, compiled, libDir, packagesDir, scratchDir, nil, "-I", "-B", "-c", pytestBootstrap)
	fmt.Print(stdout)
	if err != nil || code != 0 {
		fmt.Fprintf(os.Stderr, "python -m pytest failed: code=%d err=%v stderr=%s\n", code, err, stderr)
		os.Exit(1)
	}
	fmt.Println("pytest_module_cli PASS")

	fmt.Println("\nALL WAZERO SMOKE TESTS PASSED")
}
