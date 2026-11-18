package main

import (
	"flag"
	"io"

	"github.com/lycaon/lycaon/internal/archlint"
	"github.com/lycaon/lycaon/internal/archlint/analyzers"
)

func runVet(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("vet", flag.ContinueOnError)
	dir := fs.String("dir", ".", "module directory")
	testSupport := fs.String("test-support", "", "comma-separated patterns for test-support package paths")
	identityFields := fs.String("identity-fields", "", "comma-separated identity fields as importpath.Type.Field")
	identityOwner := fs.String("identity-owner", "", "package whose constants may name an identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := analyzers.TestImport.Flags.Set("test-support", *testSupport); err != nil {
		return err
	}
	if err := analyzers.IdentLit.Flags.Set("identity-fields", *identityFields); err != nil {
		return err
	}
	if err := analyzers.IdentLit.Flags.Set("identity-owner", *identityOwner); err != nil {
		return err
	}
	findings, err := archlint.Vet(*dir, fs.Args(), analyzers.All())
	if err != nil {
		return err
	}
	return writeJSON(out, findings)
}
