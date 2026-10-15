package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/diagnostics"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/upgradefixture"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/visualscreen/ocrfixture"
)

func runDiagnostics(ctx context.Context, args []string) error {
	if len(args) == 3 && args[0] == "upgrade-fixture-verify" {
		return upgradefixture.Verify(ctx, args[1], args[2]) //nolint:gosec // G602 — guarded by len(args) == 3
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
	if len(args) == 1 && args[0] == "ocr-engine" {
		return verifyOCREngine(ctx, os.Stderr)
	}
	if len(args) != 1 || args[0] != "startup-diagnostics" {
		return fmt.Errorf("usage: pw diagnostics startup-diagnostics | document-core | ocr-engine | schema-baseline | store-baseline <store.db> | upgrade-fixture-verify <config-dir> <manifest.json>")
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

// ocrProbeLines is the text the engine self-check must read back.
var ocrProbeLines = []string{"Painted Wolf Code", "reads this image"}

// verifyOCREngine proves the installed text recognizer resolves from this
// executable and reads a rendered fixture, under the confinement images are
// screened with.
func verifyOCREngine(ctx context.Context, out io.Writer) error {
	backend, err := visualscreen.DefaultOCRBackend()
	if err != nil {
		return err
	}
	img, _, err := ocrfixture.Render(ocrfixture.Options{Lines: ocrProbeLines})
	if err != nil {
		return fmt.Errorf("render fixture: %w", err)
	}
	raw, err := ocrfixture.PNG(img)
	if err != nil {
		return fmt.Errorf("encode fixture: %w", err)
	}
	spans, err := visualscreen.NewDefaultOCREngine().RecognizeText(ctx, "image/png", raw)
	if err != nil {
		return fmt.Errorf("recognize fixture: %w", err)
	}
	var texts []string
	for _, span := range spans {
		texts = append(texts, span.Text)
	}
	read := strings.Join(texts, "\n")
	for _, word := range []string{"Painted", "Wolf", "image"} {
		if !strings.Contains(read, word) {
			return fmt.Errorf("text recognition read %q from the fixture; %q is missing", read, word)
		}
	}
	fmt.Fprintf(out, "text recognition verified: %s (%d spans)\n", backend, len(spans))
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
