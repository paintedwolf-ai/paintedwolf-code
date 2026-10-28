// Command userlands builds, verifies and records the pinned reference userland
// images, and is the driver each of their containers runs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

const usage = `usage:
  userlands build [userland...]                  build missing images and verify every image by its recorded digest
  userlands record [-arch amd64,arm64] [userland...]  rebuild from the pins without cache and record each digest
  userlands inventory userland                   print the verified image's inventory
  userlands driver                               answer one request inside a userland container`

// Exit statuses: a failed check, a usage error, or a check that could not run.
const (
	exitFailed     = 1
	exitUsage      = 2
	exitUnverified = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, referenceuserland.NewRunner)
	stop()
	os.Exit(code)
}

type newRunner func(context.Context) (*referenceuserland.Runner, error)

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, open newRunner) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if args[0] == "driver" {
		if err := referenceuserland.ServeDriver(stdin, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, "driver:", err)
			return exitFailed
		}
		return 0
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	arches := flags.String("arch", strings.Join(referenceuserland.Architectures, ","), "architectures to record")
	if err := flags.Parse(args[1:]); err != nil {
		return exitUsage
	}
	ids, err := userlands(flags.Args())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitUsage
	}
	command := map[string]func(context.Context, *referenceuserland.Runner, []referenceuserland.ID, []string, io.Writer) error{
		"build":     build,
		"record":    record,
		"inventory": inventory,
	}[args[0]]
	if command == nil || args[0] != "record" && flags.NFlag() > 0 || args[0] == "inventory" && len(ids) != 1 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	runner, err := open(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "userlands: unverified:", err)
		return exitUnverified
	}
	defer func() { _ = runner.Close() }()
	runner.Log = stderr
	if err := command(ctx, runner, ids, strings.Split(*arches, ","), stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "userlands:", err)
		return exitFailed
	}
	return 0
}

func userlands(names []string) ([]referenceuserland.ID, error) {
	if len(names) == 0 {
		return referenceuserland.IDs, nil
	}
	ids := make([]referenceuserland.ID, 0, len(names))
	for _, name := range names {
		id := referenceuserland.ID(name)
		if !slices.Contains(referenceuserland.IDs, id) {
			return nil, fmt.Errorf("unknown userland %q; known: %v", name, referenceuserland.IDs)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func build(ctx context.Context, r *referenceuserland.Runner, ids []referenceuserland.ID, _ []string, out io.Writer) error {
	for _, id := range ids {
		inv, err := r.Build(ctx, id)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "userland %s (%s): verified %s, %d entries, %d commands\n",
			id, r.Architecture(), inv.Tree, inv.Entries, len(inv.Commands))
	}
	return nil
}

func record(ctx context.Context, r *referenceuserland.Runner, ids []referenceuserland.ID, arches []string, out io.Writer) error {
	for _, arch := range arches {
		if !slices.Contains(referenceuserland.Architectures, arch) {
			return fmt.Errorf("unknown architecture %q; known: %v", arch, referenceuserland.Architectures)
		}
	}
	if _, err := os.Stat(referenceuserland.ManifestPath); err != nil {
		return fmt.Errorf("record runs from the Go module root: %w", err)
	}
	for _, id := range ids {
		for _, arch := range arches {
			inv, err := r.Record(ctx, id, arch)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "userland %s (%s): recorded %s, %d entries\n", id, arch, inv.Tree, inv.Entries)
		}
	}
	data, err := r.Manifest().Encode()
	if err != nil {
		return err
	}
	return os.WriteFile(referenceuserland.ManifestPath, data, 0o644)
}

func inventory(ctx context.Context, r *referenceuserland.Runner, ids []referenceuserland.ID, _ []string, out io.Writer) error {
	inv, err := r.Inventory(ctx, ids[0])
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(inv)
}
