package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/archlint/xrefs"
)

func runRefs(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("refs", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	indexPath := fs.String("index", "", "file holding `git ls-files -s -z` output for root, or - for stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var index []byte
	var err error
	switch *indexPath {
	case "":
		return fmt.Errorf("refs needs -index")
	case "-":
		index, err = io.ReadAll(in)
	default:
		index, err = os.ReadFile(*indexPath)
	}
	if err != nil {
		return err
	}
	findings, err := xrefs.Refs(*root, index)
	if err != nil {
		return err
	}
	return writeJSON(out, findings)
}
