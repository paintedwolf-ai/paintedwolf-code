package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/modelstore"
)

func prepare(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("model-bundle prepare", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	directory := flags.String("bundle", "", "exported payload directory")
	model := flags.String("model", "", "model identity")
	revision := flags.String("revision", "", "immutable export revision")
	device := flags.String("device", "", "backend identity")
	primary := flags.String("artifact", "", "primary payload as NAME:FORMAT")
	var files []modelstore.Artifact
	flags.Func("file", "supplementary payload as NAME:FORMAT; repeatable", func(value string) error {
		payload, err := payloadArgument(value)
		if err != nil {
			return err
		}
		if len(files) == 64 {
			return errors.New("too many supplementary payloads")
		}
		files = append(files, payload)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *model == "" || *revision == "" || *device == "" || *primary == "" {
		return errors.New("bundle, model, revision, device and artifact are required")
	}
	artifact, err := payloadArgument(*primary)
	if err != nil {
		return err
	}
	manifest, err := modelstore.PrepareBundle(ctx, *directory, modelstore.Manifest{
		Model: *model, Revision: *revision, Device: *device, Artifact: artifact, Files: files,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(manifest)
}

func payloadArgument(value string) (modelstore.Artifact, error) {
	name, format, ok := strings.Cut(value, ":")
	if !ok || name == "" || format == "" || strings.Contains(format, ":") {
		return modelstore.Artifact{}, errors.New("payload must be NAME:FORMAT")
	}
	return modelstore.Artifact{Name: name, Format: format}, nil
}
