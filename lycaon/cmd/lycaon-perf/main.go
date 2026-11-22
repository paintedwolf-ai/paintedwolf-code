// Command lycaon-perf drives a release sidecar through representative local
// workloads and reports latency, which it never judges, with resource,
// correctness and work counts, which it gates on.
//
//	lycaon-perf [flags]           sidecar workload
//	lycaon-perf scale [flags]     bounded work on the scale fixture, at two sizes
//	lycaon-perf tree --root DIR   write the scale fixture's source tree
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/scalefixture"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "scale":
			os.Exit(scaleMain(os.Args[2:], os.Stdout))
		case "tree":
			os.Exit(treeMain(os.Args[2:], os.Stdout))
		}
	}
	os.Exit(sidecarMain(os.Args[1:], os.Stdout))
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func treeMain(args []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("tree", flag.ContinueOnError)
	root := flags.String("root", "", "empty or absent directory to write the tree into")
	files := flags.Int("files", scalefixture.LargeFiles, "files to write")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "lycaon-perf tree: --root is required")
		return 2
	}
	shape, err := scalefixture.WriteTree(*root, *files)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf tree: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Scale fixture tree: %d files in %d directories, %d bytes, at %s\n", shape.Files, shape.Directories, shape.Bytes, *root)
	return 0
}

func sidecarMain(args []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("lycaon-perf", flag.ContinueOnError)
	var cfg runConfig
	flags.StringVar(&cfg.binary, "binary", "", "release sidecar binary to run")
	flags.StringVar(&cfg.output, "output", "", "report JSON path")
	flags.StringVar(&cfg.scale, "scale", "medium", "fixture scale: small, medium, or large")
	flags.IntVar(&cfg.iterations, "iterations", 20, "warm repetitions per query scenario")
	flags.IntVar(&cfg.prompts, "prompts", 8, "sequential mock-provider prompt turns")
	flags.DurationVar(&cfg.soak, "soak", 0, "repeat mixed work for this duration")
	flags.BoolVar(&cfg.keep, "keep", false, "keep the scratch run directory")
	flags.BoolVar(&cfg.installed, "installed", false,
		"drive an installed release engine in a disposable account whose release configuration does not exist yet")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if cfg.binary == "" {
		fmt.Fprintln(os.Stderr, "lycaon-perf: --binary is required")
		return 2
	}
	if cfg.output == "" {
		cfg.output = fmt.Sprintf("sidecar-perf-%s.json", time.Now().UTC().Format("20060102T150405Z"))
	}
	if cfg.installed && cfg.soak > 0 {
		fmt.Fprintln(os.Stderr, "lycaon-perf: --installed measures release-valid scenarios; --soak needs the mock provider")
		return 2
	}
	if cfg.iterations < 1 || cfg.prompts < 1 || cfg.soak < 0 {
		fmt.Fprintln(os.Stderr, "lycaon-perf: --iterations and --prompts must be positive; --soak cannot be negative")
		return 2
	}
	ctx, stop := signalContext()
	report, err := run(ctx, cfg)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf: %v\n", err)
		return 1
	}
	if err := writeJSON(cfg.output, report); err != nil {
		fmt.Fprintf(os.Stderr, "lycaon-perf: write report: %v\n", err)
		return 1
	}
	printReport(stdout, report, cfg.output)
	if len(report.Violations) > 0 {
		return 1
	}
	return 0
}
