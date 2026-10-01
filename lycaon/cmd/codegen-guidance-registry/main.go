// Command codegen-guidance-registry combines stock policy catalogs into the registry.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
)

func main() {
	root := flag.String("root", ".", "lycaon module root (contains config/) or repo root")
	check := flag.Bool("check", false, "exit 1 if guidance_registry.json is stale")
	flag.Parse()

	abs, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	moduleRoot, registryPath, err := resolvePaths(abs)
	if err != nil {
		fatal(err)
	}

	if err := validateStockPolicy(moduleRoot); err != nil {
		fatal(err)
	}

	if *check {
		have, err := os.ReadFile(registryPath) // #nosec G304 -- path from resolvePaths (module/repo root)
		if err != nil {
			fatal(fmt.Errorf("stale %s — run ./task codegen:guidance-registry", registryPath))
		}
		tmp, err := os.MkdirTemp("", "guidance-registry-check-*")
		if err != nil {
			fatal(err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		out := filepath.Join(tmp, "guidance_registry.json")
		if err := oar.SyncRegistryFromStock(out); err != nil {
			fatal(err)
		}
		want, err := os.ReadFile(out) // #nosec G304 -- temp path under MkdirTemp
		if err != nil {
			fatal(err)
		}
		if !bytes.Equal(have, want) {
			fatal(fmt.Errorf("stale %s — run ./task codegen:guidance-registry", registryPath))
		}
		return
	}

	if err := oar.SyncRegistryFromStock(registryPath); err != nil {
		fatal(err)
	}
	fmt.Println("wrote", registryPath)
}

// resolvePaths accepts --root as either the lycaon module dir or the repo root.
func resolvePaths(abs string) (moduleRoot, registryPath string, err error) {
	repoSchemas := filepath.Join(filepath.Clean(filepath.Join(abs, "..")), "schemas", "guidance_registry.json")
	if _, e := os.Stat(repoSchemas); e == nil {
		return abs, repoSchemas, nil
	}
	alt := filepath.Join(abs, "schemas", "guidance_registry.json")
	if _, e := os.Stat(alt); e == nil {
		return filepath.Join(abs, "lycaon"), alt, nil
	}
	return "", "", fmt.Errorf("schemas/guidance_registry.json not found from --root %s", abs)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// Validate the same captured stock catalog that will supply registry metadata.
func validateStockPolicy(moduleRoot string) error {
	ctx := context.Background()
	catalog, err := extpacks.ResolveStockCatalog(ctx, nil)
	if err != nil {
		return fmt.Errorf("resolve stock policy: %w", err)
	}
	extpacks.SetActive(catalog)
	if _, err := catalogview.Build(ctx, moduleRoot, catalog); err != nil {
		return fmt.Errorf("validate stock catalog before registry generation: %w", err)
	}
	return nil
}
