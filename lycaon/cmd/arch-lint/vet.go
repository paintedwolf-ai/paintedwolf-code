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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := analyzers.TestImport.Flags.Set("test-support", *testSupport); err != nil {
		return err
	}
	findings, err := archlint.Vet(*dir, fs.Args(), analyzers.All)
	if err != nil {
		return err
	}
	return writeJSON(out, findings)
}
