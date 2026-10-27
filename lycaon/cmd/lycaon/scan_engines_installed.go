package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/scan/installed"
)

func runVerifyInstalledEngine(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("scan engines verify-installed", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "installed engine payload root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" {
		return fmt.Errorf("usage: pw scan engines verify-installed --root DIR")
	}
	report, err := installed.Verify(ctx, *root)
	if outputErr := json.NewEncoder(out).Encode(report); outputErr != nil {
		return outputErr
	}
	return err
}
