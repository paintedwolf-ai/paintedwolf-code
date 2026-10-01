// Command lycaon-perf drives a release sidecar through representative local
// workloads and emits a machine-readable latency, resource, and correctness report.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var cfg runConfig
	flag.StringVar(&cfg.binary, "binary", "", "release sidecar binary to run")
	flag.StringVar(&cfg.output, "output", "", "report JSON path")
	flag.StringVar(&cfg.scale, "scale", "medium", "fixture scale: small, medium, or large")
	flag.IntVar(&cfg.iterations, "iterations", 20, "warm repetitions per query scenario")
	flag.IntVar(&cfg.prompts, "prompts", 8, "sequential mock-provider prompt turns")
	flag.DurationVar(&cfg.soak, "soak", 0, "repeat mixed work for this duration")
	flag.BoolVar(&cfg.enforce, "enforce", false, "fail when a performance budget is exceeded")
	flag.BoolVar(&cfg.keep, "keep", false, "keep the scratch run directory")
	flag.Parse()

	if cfg.binary == "" {
		fmt.Fprintln(os.Stderr, "lycaon-perf: --binary is required")
		os.Exit(2)
	}
	if cfg.output == "" {
		cfg.output = fmt.Sprintf("sidecar-perf-%s.json", time.Now().UTC().Format("20060102T150405Z"))
	}
	if cfg.iterations < 1 || cfg.prompts < 1 || cfg.soak < 0 {
		fmt.Fprintln(os.Stderr, "lycaon-perf: --iterations and --prompts must be positive; --soak cannot be negative")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	report, err := run(ctx, cfg)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf: %v\n", err)
		os.Exit(1)
	}
	if err := writeReport(cfg.output, report); err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf: write report: %v\n", err)
		os.Exit(1)
	}
	printReport(os.Stdout, report, cfg.output)
	if cfg.enforce && len(report.Violations) > 0 {
		os.Exit(1)
	}
}
