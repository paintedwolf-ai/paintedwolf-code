package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/scan/bundled"
)

func (opts options) fetchOptions() bundled.ReleaseFetchOptions {
	return bundled.ReleaseFetchOptions{CacheRoot: opts.cacheRoot, Archive: opts.archive, Offline: opts.offline}
}

func (opts options) validateRelease() error {
	if opts.cacheRoot == "" || opts.root != "" || opts.directory != "" || opts.executable != "" || opts.digest != "" || opts.dirOnly || os.Getenv(bundled.EnvOpenGrepCandidate) != "" {
		return fmt.Errorf("release fetch and selection require cache-root and cannot use staging or candidate overrides")
	}
	if opts.mode == "fetch" {
		if opts.releaseManifest != "" || opts.manifestOutput != "" || opts.tag != "" || opts.expectedCommit != "" {
			return fmt.Errorf("descriptor, output and attestation identity require select mode")
		}
		return nil
	}
	if opts.releaseManifest == "" || opts.manifestOutput == "" || opts.target != "" || opts.tag == "" || opts.expectedCommit == "" {
		return fmt.Errorf("select requires release-manifest, manifest-output, tag, expected-commit and current-host qualification")
	}
	if opts.offline {
		return fmt.Errorf("new selection requires online immutable-release verification; checked-in pins support offline fetch")
	}
	return nil
}

func selectRelease(opts options) (string, error) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := bundled.SelectAttestedRelease(ctx, bundled.AttestedReleaseSelection{
		Descriptor: opts.releaseManifest, Tag: opts.tag, ExpectedCommit: opts.expectedCommit,
		Destination: opts.manifestOutput, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Fetch: opts.fetchOptions(),
	}); err != nil {
		return "", err
	}
	return filepath.Abs(opts.manifestOutput)
}
