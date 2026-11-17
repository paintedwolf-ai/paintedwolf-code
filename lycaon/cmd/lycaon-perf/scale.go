package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/scalefixture"
)

// scaleSize is one fixture size. The large size is a project of 500,000 files
// and a store of at least 5 GiB, of which at least 1,000,000 pages are free.
type scaleSize struct {
	Name      string `json:"name"`
	Files     int    `json:"files"`
	FreePages int64  `json:"free_pages"`
}

var scaleSizes = [2]scaleSize{
	{Name: "small", Files: scalefixture.LargeFiles / 10, FreePages: 131_072},
	{Name: "large", Files: scalefixture.LargeFiles, FreePages: 1_310_720},
}

type scaleConfig struct {
	binary, output, work, corpus string
	divisor                      int
	keep                         bool
}

type scaleReport struct {
	Version    int                `json:"version"`
	StartedAt  time.Time          `json:"started_at"`
	Sizes      []scaleMeasurement `json:"sizes"`
	Bounds     map[string]float64 `json:"bounds"`
	Violations []string           `json:"violations"`
}

type scaleMeasurement struct {
	Size    scaleSize          `json:"size"`
	Fixture scaleFixture       `json:"fixture"`
	Boot    scaleBoot          `json:"boot"`
	Vacuum  scaleVacuum        `json:"vacuum"`
	Delete  scaleDelete        `json:"delete"`
	Upgrade scaleUpgrade       `json:"upgrade"`
	Elapsed map[string]float64 `json:"elapsed_seconds"`
}

type scaleFixture struct {
	Tree           scalefixture.Shape `json:"tree"`
	IndexedFiles   int64              `json:"indexed_files"`
	PageSize       int64              `json:"page_size"`
	StorePages     int64              `json:"store_pages"`
	FreePages      int64              `json:"free_pages"`
	StoreFileBytes int64              `json:"store_file_bytes"`
}

type scaleBoot struct {
	Work        map[string]int64 `json:"work"`
	FilesOpened int64            `json:"files_opened"`
}

type scaleVacuum struct {
	FreePagesBefore int64 `json:"free_pages_before"`
	FreePagesAfter  int64 `json:"free_pages_after"`
	PassPages       int64 `json:"pass_pages"`
	Passes          int64 `json:"passes"`
	Turns           int64 `json:"turns"`
	PeakPassPages   int64 `json:"peak_pass_pages"`
	PagesReclaimed  int64 `json:"pages_reclaimed"`
}

type scaleDelete struct {
	OwnedRows int64            `json:"owned_rows"`
	Work      map[string]int64 `json:"work"`
}

type scaleUpgrade struct {
	Entries    int              `json:"entries"`
	StorePages int64            `json:"store_pages"`
	Work       map[string]int64 `json:"work"`
}

func scaleMain(args []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("scale", flag.ContinueOnError)
	var cfg scaleConfig
	flags.StringVar(&cfg.binary, "binary", "", "release sidecar binary to run")
	flags.StringVar(&cfg.output, "output", "", "report JSON path")
	flags.StringVar(&cfg.work, "work", os.TempDir(), "directory with room for the large fixture, about 12 GB")
	flags.StringVar(&cfg.corpus, "corpus", filepath.Join("testdata", "upgrade-corpus", "1.0.2"), "released store the upgrade starts from")
	flags.IntVar(&cfg.divisor, "divisor", 1, "divide both sizes for a trial run; the gate uses 1")
	flags.BoolVar(&cfg.keep, "keep", false, "keep each size's fixture")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if cfg.binary == "" || cfg.output == "" || cfg.divisor < 1 {
		fmt.Fprintln(os.Stderr, "lycaon-perf scale: --binary and --output are required; --divisor must be positive")
		return 2
	}
	ctx, stop := signalContext()
	defer stop()
	report, err := runScale(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf scale: %v\n", err)
		return 1
	}
	if err := writeJSON(cfg.output, report); err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf scale: write report: %v\n", err)
		return 1
	}
	printScaleReport(stdout, report, cfg.output)
	if len(report.Violations) > 0 {
		return 1
	}
	return 0
}

func runScale(ctx context.Context, cfg scaleConfig) (scaleReport, error) {
	report := scaleReport{Version: 1, StartedAt: time.Now().UTC(), Bounds: scaleBounds()}
	for _, size := range scaleSizes {
		size.Files /= cfg.divisor
		size.FreePages /= int64(cfg.divisor)
		measured, err := measureSize(ctx, cfg, size)
		if err != nil {
			return report, fmt.Errorf("%s fixture: %w", size.Name, err)
		}
		report.Sizes = append(report.Sizes, measured)
	}
	report.Violations = evaluateScale(report.Sizes[0], report.Sizes[1])
	return report, nil
}

func measureSize(ctx context.Context, cfg scaleConfig, size scaleSize) (scaleMeasurement, error) {
	dir, err := os.MkdirTemp(cfg.work, "painted-wolf-scale-"+size.Name+"-*")
	if err != nil {
		return scaleMeasurement{}, err
	}
	if !cfg.keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}
	f := &scaleFixtureRun{cfg: cfg, size: size, dir: dir, measured: scaleMeasurement{Size: size, Elapsed: map[string]float64{}}}
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"tree", f.writeTree}, {"populate", f.populate}, {"history", f.recordHistory}, {"free_pages", f.freePages},
		{"boot_and_vacuum", f.bootAndVacuum}, {"delete", f.deleteProject}, {"upgrade", f.upgrade},
	} {
		started := time.Now()
		if err := step.run(ctx); err != nil {
			return f.measured, fmt.Errorf("%s: %w", step.name, err)
		}
		f.measured.Elapsed[step.name] = time.Since(started).Seconds()
	}
	return f.measured, nil
}

func printScaleReport(w io.Writer, report scaleReport, path string) {
	for _, m := range report.Sizes {
		fmt.Fprintf(w, "Scale %s: %d files, %d store pages, %d free\n", m.Size.Name, m.Fixture.Tree.Files, m.Fixture.StorePages, m.Fixture.FreePages)
		fmt.Fprintf(w, "  boot: %d statements, %d pages fetched, %d rows returned, %d project files opened\n",
			m.Boot.Work["statements"], m.Boot.Work["pages_fetched"], m.Boot.Work["rows_returned"], m.Boot.FilesOpened)
		fmt.Fprintf(w, "  vacuum: %d passes in %d turns for %d free pages, peak %d pages per pass\n",
			m.Vacuum.Passes, m.Vacuum.Turns, m.Vacuum.FreePagesBefore, m.Vacuum.PeakPassPages)
		fmt.Fprintf(w, "  delete: %d owned rows, %d changed, %d statements, peaks %d statement / %d transaction / %d returned\n",
			m.Delete.OwnedRows, m.Delete.Work["rows_changed"], m.Delete.Work["statements"],
			m.Delete.Work["peak_statement_rows_changed"], m.Delete.Work["peak_transaction_rows_changed"], m.Delete.Work["peak_statement_rows_returned"])
		fmt.Fprintf(w, "  upgrade: %d entries, %d store pages, %d pages fetched, peak %d rows returned, peak resident %d bytes\n",
			m.Upgrade.Entries, m.Upgrade.StorePages, m.Upgrade.Work["pages_fetched"], m.Upgrade.Work["peak_statement_rows_returned"], m.Upgrade.Work["peak_resident_bytes"])
	}
	if len(report.Violations) > 0 {
		fmt.Fprintln(w, "Bound violations:")
		for _, violation := range report.Violations {
			fmt.Fprintf(w, "  - %s\n", violation)
		}
	}
	fmt.Fprintf(w, "Report: %s\n", path)
}
