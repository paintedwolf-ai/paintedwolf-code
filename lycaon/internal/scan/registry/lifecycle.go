package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

// Reload atomically rebuilds scanner adapters after a device catalog mutation.
func (r *Impl) Reload() error {
	if r == nil {
		return fmt.Errorf("scan registry not configured")
	}
	ctx, finish, err := r.work.Begin(context.Background())
	if err != nil {
		return err
	}
	defer finish()
	cfg := r.static
	if cfg == nil {
		var err error
		cfg, err = scancatalog.LoadMergedScannerConfig(r.opts.ModuleRoot, "", r.home)
		if err != nil {
			return err
		}
	}
	manifest, err := bundled.LoadManifest()
	if err != nil {
		return fmt.Errorf("bundled manifest: %w", err)
	}
	if err := bundled.ValidateManifest(manifest); err != nil {
		return err
	}
	values, err := buildScannerGeneration(ctx, cfg, r.opts, r.home, manifest)
	if err != nil {
		return err
	}
	r.lifecycleMu.Lock()
	if err := ctx.Err(); err != nil {
		r.lifecycleMu.Unlock()
		_ = closeScanners(context.WithoutCancel(ctx), scannerValues(values))
		return err
	}
	previous := r.adapters()
	r.scannerRegistry().Replace(values)
	retirementCtx, retired, _ := r.work.Begin(context.Background())
	r.lifecycleMu.Unlock()
	// Replaced adapters finish in-flight scans before their workers stop.
	go func() {
		defer retired()
		if err := retireScanners(retirementCtx, previous); err != nil {
			slog.Warn("stop replaced scanner adapters", "error", err)
		}
	}()
	return nil
}

// Close seals catalog changes and joins every current or retiring adapter.
func (r *Impl) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.lifecycleMu.Lock()
		r.work.Stop()
		r.lifecycleMu.Unlock()
		r.closeErr = r.work.Wait(context.WithoutCancel(ctx))
		r.closeErr = errors.Join(r.closeErr, closeScanners(ctx, r.adapters()))
	})
	return r.closeErr
}

func scannerValues(values map[string]scan.CodeScanner) []scan.CodeScanner {
	out := make([]scan.CodeScanner, 0, len(values))
	for _, scanner := range values {
		out = append(out, scanner)
	}
	return out
}

func closeScanners(ctx context.Context, scanners []scan.CodeScanner) error {
	var errs []error
	for _, s := range scanners {
		if closer, ok := s.(interface{ Close(context.Context) error }); ok {
			errs = append(errs, closer.Close(ctx))
		} else if closer, ok := s.(io.Closer); ok {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}

func retireScanners(ctx context.Context, scanners []scan.CodeScanner) error {
	var errs []error
	for _, scanner := range scanners {
		if retiring, ok := scanner.(interface{ Retire(context.Context) error }); ok {
			errs = append(errs, retiring.Retire(ctx))
		} else if closer, ok := scanner.(io.Closer); ok {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}
