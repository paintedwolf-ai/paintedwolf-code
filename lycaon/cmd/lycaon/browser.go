package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/browserengine"
)

func runBrowser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw browser ensure [--cache-dir DIR]")
	}
	switch args[0] {
	case "ensure":
		return runBrowserEnsure(ctx, args[1:])
	default:
		return fmt.Errorf("unknown browser subcommand %q", args[0])
	}
}

func runBrowserEnsure(ctx context.Context, args []string) error {
	cacheDir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cache-dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--cache-dir requires a path")
			}
			cacheDir = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown argument %q", args[i])
		}
	}
	if cacheDir == "" {
		// The same resolver serve uses, so provisioning cannot fill a directory
		// the engine will not look in.
		cacheDir = browserengine.ManagedCacheDir()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	// `pw browser ensure` is the provisioning entry point — ./task
	// browser:ensure and den-build-bundle.sh — so this is the one caller allowed
	// to download.
	resolved, err := browserengine.EnsureBinary(ctx, browserengine.ResolveOptions{CacheDir: cacheDir, AllowDownload: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "browser ready source=%s path=%s\n", resolved.Source, resolved.Path)
	return nil
}
