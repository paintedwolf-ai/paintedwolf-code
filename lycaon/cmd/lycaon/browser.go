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
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		switch arg {
		case "--json":
			jsonOutput = true
		case "--cache-dir":
			if len(args) == 0 {
				return fmt.Errorf("--cache-dir requires a path")
			}
			cacheDir = args[0]
			args = args[1:]
		default:
			return fmt.Errorf("unknown argument %q", arg)
		}
	}
	if cacheDir == "" {
		// Provision and serve from the same cache.
		cacheDir = browserengine.ManagedCacheDir()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	// Provisioning permits downloads; serving only resolves installed binaries.
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
