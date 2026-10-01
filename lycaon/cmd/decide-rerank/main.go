// Command decide-rerank measures the decision engine at every reranking site.
// It harvests code units from a repository, drives each site through its real
// entry point with the engine attached, and reports ranking quality and
// latency against the lexical order the site computes on its own. It is a
// bespoke experiment tool and never runs inside verification.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lycaon/lycaon/internal/observability"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}
	// The seam and the engine client log through slog; LYCAON_LOG_LEVEL=debug
	// also relays the engine's own stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: observability.ParseLogLevel(os.Getenv("LYCAON_LOG_LEVEL"))})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "harvest":
		err = runHarvest(ctx, os.Args[2:])
	case "eval":
		err = runEval(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "decide-rerank: %v\n", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  decide-rerank harvest --repo PATH --name NAME --out units.jsonl [--include DIR,...] [--max-files N]
  decide-rerank eval --site SITE --repo PATH --name NAME --units units.jsonl --pairs pairs.jsonl
                     [--json report.json] [--dump rows.jsonl] [--limit N]
                     [--no-engine] [--weight W] [--k N] [--chunk N] [--deadline D]

sites: summarize_structure, summarize_definitions, repomap_tags, project_search`)
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}
