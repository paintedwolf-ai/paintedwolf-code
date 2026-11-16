package main

import (
	"flag"
	"io"

	"github.com/lycaon/lycaon/internal/archlint/xrefs"
)

func runRefs(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("refs", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	findings, err := xrefs.Refs(*root)
	if err != nil {
		return err
	}
	return writeJSON(out, findings)
}
