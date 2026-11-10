package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/browserengine"
)

func runBrowser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw browser ensure [--cache-dir DIR] [--json]")
	}
	switch args[0] {
	case "ensure":
		return runBrowserEnsure(ctx, args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown browser subcommand %q", args[0])
	}
}

func runBrowserEnsure(ctx context.Context, args []string, out io.Writer) error {
	cacheDir := ""
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
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
	if jsonOutput {
		return json.NewEncoder(out).Encode(map[string]string{"path": resolved.Path, "source": resolved.Source})
	}
	fmt.Fprintf(out, "browser ready source=%s path=%s\n", resolved.Source, resolved.Path)
	return nil
}
