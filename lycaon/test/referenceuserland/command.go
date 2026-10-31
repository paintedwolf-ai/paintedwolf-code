package referenceuserland

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
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

// Command runs the userlands command line: build, record, inventory, or driver.
// It returns the process exit status.
func Command(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runCommand(ctx, args, stdin, stdout, stderr, NewRunner)
}

type openRunner func(context.Context) (*Runner, error)

func runCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, open openRunner) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if args[0] == "driver" {
		if err := ServeDriver(stdin, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, "driver:", err)
			return exitFailed
		}
		return 0
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	arches := flags.String("arch", strings.Join(Architectures, ","), "architectures to record")
	if err := flags.Parse(args[1:]); err != nil {
		return exitUsage
	}
	ids, err := userlandIDs(flags.Args())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitUsage
	}
	run := map[string]func(context.Context, *Runner, []ID, []string, io.Writer) error{
		"build":     buildCommand,
		"record":    recordCommand,
		"inventory": inventoryCommand,
	}[args[0]]
	if run == nil || args[0] != "record" && flags.NFlag() > 0 || args[0] == "inventory" && len(ids) != 1 {
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
	if err := run(ctx, runner, ids, strings.Split(*arches, ","), stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "userlands:", err)
		return exitFailed
	}
	return 0
}

func userlandIDs(names []string) ([]ID, error) {
	if len(names) == 0 {
		return IDs, nil
	}
	ids := make([]ID, 0, len(names))
	for _, name := range names {
		id := ID(name)
		if !slices.Contains(IDs, id) {
			return nil, fmt.Errorf("unknown userland %q; known: %v", name, IDs)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func buildCommand(ctx context.Context, r *Runner, ids []ID, _ []string, out io.Writer) error {
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

func recordCommand(ctx context.Context, r *Runner, ids []ID, arches []string, out io.Writer) error {
	for _, arch := range arches {
		if !slices.Contains(Architectures, arch) {
			return fmt.Errorf("unknown architecture %q; known: %v", arch, Architectures)
		}
	}
	if _, err := os.Stat(ManifestPath); err != nil {
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
	return os.WriteFile(ManifestPath, data, 0o644)
}

func inventoryCommand(ctx context.Context, r *Runner, ids []ID, _ []string, out io.Writer) error {
	inv, err := r.Inventory(ctx, ids[0])
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(inv)
}
