package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/scan/bundled"
)

func runVerifyBundledEngine(args []string) error {
	flags := flag.NewFlagSet("scan engines verify-bundled", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "staged engine root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" {
		return fmt.Errorf("usage: pw scan engines verify-bundled --root DIR")
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	manifest, err := bundled.LoadManifest()
	if err != nil {
		return err
	}
	path, err := bundled.VerifyStagedArtifact(manifest, absolute, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}
