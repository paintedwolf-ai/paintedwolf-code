package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/diagnostics"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/upgradefixture"
)

func runDiagnostics(ctx context.Context, args []string) error {
	if len(args) == 3 && args[0] == "upgrade-fixture-verify" {
		return upgradefixture.Verify(ctx, args[1], args[2])
	}
	if len(args) == 1 && args[0] == "schema-baseline" {
		baseline, err := db.CurrentBaseline(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(baseline)
	}
	if len(args) == 2 && args[0] == "store-baseline" {
		return writeStoreBaseline(ctx, args[1], os.Stdout)
	}
	if len(args) == 1 && args[0] == "document-core" {
		return verifyDocumentCore(ctx)
	}
	if len(args) != 1 || args[0] != "startup-diagnostics" {
		return fmt.Errorf("usage: pw diagnostics startup-diagnostics | document-core | schema-baseline | store-baseline <store.db> | upgrade-fixture-verify <config-dir> <manifest.json>")
	}
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("diagnostics config directory: %w", err)
	}
	return writeStartupDiagnostics(ctx, args, dir, os.Stdout, time.Now())
}

// verifyDocumentCore proves the installed document core runs from this
// executable, under the confinement and code signing editors run it with.
func verifyDocumentCore(ctx context.Context) error {
	verified, err := documentcore.Verify(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "document core verified: %s (confined=%t)\n", verified.Binary, verified.Confined)
	return nil
}

// writeStartupDiagnostics writes one startup diagnostics archive.
func writeStartupDiagnostics(
	ctx context.Context,
	args []string,
	configDir string,
	out io.Writer,
	generatedAt time.Time,
) error {
	if len(args) != 1 || args[0] != "startup-diagnostics" {
		return fmt.Errorf("usage: pw diagnostics startup-diagnostics")
	}
	raw, err := diagnostics.BuildStartup(ctx, configDir, generatedAt)
	if err != nil {
		return err
	}
	if _, err := out.Write(raw); err != nil {
		return fmt.Errorf("write startup diagnostics: %w", err)
	}
	return nil
}
