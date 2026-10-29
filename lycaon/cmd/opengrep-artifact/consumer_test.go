package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConsumerModesRequireSeparateProducerAndFinalResources(t *testing.T) {
	t.Setenv("LYCAON_OPENGREP_CANDIDATE", "")
	finalize := options{mode: "finalize-consumer", directory: "/producer", root: "/final", consumerRecipe: "/recipe.json", evidenceRoot: "/evidence", auditOutput: "/audit.json"}
	audit := options{mode: "audit", directory: "/producer", packagedDirectory: "/final/bundled/opengrep", consumerIdentity: "/identity.txt", evidenceRoot: "/evidence", auditOutput: "/audit.json"}
	testutil.FailErr(t, "admit explicit finalization arguments", finalize.validate())
	testutil.FailErr(t, "admit separate consumer audit arguments", audit.validate())
	for name, mutate := range map[string]func(*options){
		"missing producer":              func(opts *options) { opts.directory = "" },
		"missing final root":            func(opts *options) { opts.root = "" },
		"missing recipe":                func(opts *options) { opts.consumerRecipe = "" },
		"missing evidence":              func(opts *options) { opts.evidenceRoot = "" },
		"missing audit":                 func(opts *options) { opts.auditOutput = "" },
		"independent executable":        func(opts *options) { opts.executable = "/other" },
		"unqualified consumer identity": func(opts *options) { opts.consumerIdentity = "/identity" },
		"ambiguous packaged directory":  func(opts *options) { opts.packagedDirectory = "/other" },
		"recipe in identity mode":       func(opts *options) { opts.mode = "identity" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := finalize
			mutate(&opts)
			if err := opts.validate(); err == nil {
				t.Fatal("ambiguous consumer build inputs accepted")
			}
		})
	}
}

func TestConsumerRecipeRequiresOnlyConsumerModes(t *testing.T) {
	t.Setenv("LYCAON_OPENGREP_CANDIDATE", "")
	if err := (options{mode: "stage", root: "/stage", consumerRecipe: "/recipe.json"}).validate(); err == nil {
		t.Fatal("consumer recipe admitted outside consumer finalization")
	}
	if err := (options{mode: "verify", root: "/stage", consumerIdentity: "/identity.txt"}).validate(); err == nil {
		t.Fatal("consumer identity admitted outside audit")
	}
}

func consumerRecipeFixture(t *testing.T, recipe bundled.ConsumerRecipe) string {
	t.Helper()
	raw, err := json.Marshal(recipe)
	testutil.FailErr(t, "encode consumer recipe fixture", err)
	path := filepath.Join(t.TempDir(), "recipe.json")
	testutil.FailErr(t, "write consumer recipe fixture", os.WriteFile(path, raw, 0o600))
	return path
}

func TestConsumerFinalizationRefusesUnqualifiedInputs(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "patch-fixture")
	testutil.FailErr(t, "write tool fixture", os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700))
	valid := bundled.ConsumerRecipe{SchemaVersion: 1, Kind: "nix-nuitka-relocation", SourceCommit: strings.Repeat("a", 40), FlakeLockSHA256: strings.Repeat("b", 64),
		ToolPath: tool, ToolSHA256: strings.Repeat("c", 64), Arguments: []string{"--set-interpreter", "/nix/store/fixture-glibc/lib/ld.so", "--set-rpath", "/nix/store/fixture-glibc/lib"}}
	unversioned := valid
	unversioned.SchemaVersion = 2
	source, err := bundled.LoadManifest()
	testutil.FailErr(t, "load source selection without producer authority", err)
	identity := filepath.Join(t.TempDir(), "identity.txt")
	testutil.FailErr(t, "write unfinalized identity fixture", os.WriteFile(identity, []byte("unfinalized identity\n"), 0o600))
	for name, opts := range map[string]options{
		"missing recipe":          {mode: "finalize-consumer", consumerRecipe: filepath.Join(t.TempDir(), "missing.json")},
		"unversioned recipe":      {mode: "finalize-consumer", consumerRecipe: consumerRecipeFixture(t, unversioned)},
		"relative relocation":     {mode: "relocate-consumer", root: "relative", consumerRecipe: consumerRecipeFixture(t, valid)},
		"unadmitted producer":     {mode: "finalize-consumer", root: t.TempDir(), consumerRecipe: consumerRecipeFixture(t, valid)},
		"missing audit identity":  {mode: "audit", consumerIdentity: filepath.Join(t.TempDir(), "missing.txt")},
		"unadmitted audit source": {mode: "audit", consumerIdentity: identity, packagedDirectory: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			if opts.mode == "audit" {
				if _, err := selectConsumerAudit(t.Context(), source, opts); err == nil {
					t.Fatal("consumer audit admitted without finalized authority")
				}
				return
			}
			if _, err := finalizeConsumer(t.Context(), source, opts); err == nil {
				t.Fatal("consumer finalization admitted without qualified inputs")
			}
		})
	}
}

func TestConsumerBuildInputsRemainBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.txt")
	testutil.FailErr(t, "write bounded input", os.WriteFile(path, []byte("public identity"), 0o600))
	raw, err := readConsumerInput(path, 15)
	testutil.FailErr(t, "read bounded input", err)
	if string(raw) != "public identity" {
		t.Fatal("build input changed")
	}
	if _, err := readConsumerInput(path, 3); err == nil {
		t.Fatal("oversized build input accepted")
	}
	if _, err := readConsumerInput(filepath.Join(t.TempDir(), "missing"), 15); err == nil {
		t.Fatal("missing build input accepted")
	}
}
