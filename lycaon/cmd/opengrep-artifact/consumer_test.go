package main

import (
	"os"
	"path/filepath"
	"testing"

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
