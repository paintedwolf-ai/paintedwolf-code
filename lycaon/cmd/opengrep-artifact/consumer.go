package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/installed"
)

func readConsumerInput(path string, limit int64) ([]byte, error) {
	// #nosec G304 -- explicit public release-build identity or recipe input.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("consumer build input exceeds its bound")
	}
	return raw, nil
}

func finalizeConsumer(ctx context.Context, producer *bundled.Manifest, opts options) (string, error) {
	raw, err := readConsumerInput(opts.consumerRecipe, 64<<10)
	if err != nil {
		return "", err
	}
	recipe, err := bundled.ParseConsumerRecipe(raw)
	if err != nil {
		return "", err
	}
	if opts.mode == "relocate-consumer" {
		if err := relocateConsumer(ctx, producer, opts, recipe); err != nil {
			return "", err
		}
	}
	selected, err := bundled.FinalizeConsumerArtifact(ctx, producer, opts.root, recipe, func(ctx context.Context, manifest *bundled.Manifest) (json.RawMessage, error) {
		report, err := installed.VerifyArtifact(ctx, opts.root, manifest)
		if err != nil {
			return nil, err
		}
		return json.Marshal(report)
	})
	if err != nil {
		return "", err
	}
	directory := bundled.EngineBundledDir(opts.root, selected.OpenGrep.Version)
	if err := bundled.WriteReleaseAudit(ctx, selected, directory, opts.evidenceRoot, opts.auditOutput); err != nil {
		return "", err
	}
	return bundled.EncodedBuildIdentity(selected)
}

func selectConsumerAudit(ctx context.Context, producer *bundled.Manifest, opts options) (*bundled.Manifest, error) {
	raw, err := readConsumerInput(opts.consumerIdentity, 32<<10)
	if err != nil {
		return nil, err
	}
	return bundled.SelectConsumerArtifact(ctx, producer, opts.packagedDirectory, strings.TrimSpace(string(raw)))
}
