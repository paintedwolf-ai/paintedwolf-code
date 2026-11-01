// model-bundle verifies a release seed before packaging.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lycaon/lycaon/internal/modelstore"
)

func run(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("model-bundle", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	directory := flags.String("bundle", "", "packaged model directory")
	keys := flags.String("authority", "", "trusted release-key registry")
	model := flags.String("model", "", "required model identity")
	device := flags.String("device", "", "required backend identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *keys == "" || *model == "" || *device == "" {
		return errors.New("bundle, authority, model and device are required")
	}
	file, err := os.Open(*keys)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, modelstore.MaxAuthorityBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return err
	}
	authority, err := modelstore.DecodeAuthority(raw)
	if err != nil {
		return err
	}
	signed, err := modelstore.VerifyBundle(ctx, *directory, authority, modelstore.Slot{Model: *model, Device: *device})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(signed)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "model-bundle:", err)
		os.Exit(1)
	}
}
