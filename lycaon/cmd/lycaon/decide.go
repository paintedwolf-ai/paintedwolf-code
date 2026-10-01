package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/decide/bialy"
)

func runDecide(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw decide ensure [--model-dir DIR]")
	}
	switch args[0] {
	case "ensure":
		return runDecideEnsure(ctx, args[1:])
	default:
		return fmt.Errorf("unknown decide subcommand %q", args[0])
	}
}

// runDecideEnsure provisions the shipped decision checkpoint; release staging bundles
// the directory it reports.
func runDecideEnsure(ctx context.Context, args []string) error {
	dir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model-dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--model-dir requires a path")
			}
			dir = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown argument %q", args[i])
		}
	}
	if dir == "" {
		dir = bialy.ManagedModelDir()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	model := bialy.ShippedModel
	lastReport := time.Time{}
	progress := func(path string, done, total int64) {
		if done < total && time.Since(lastReport) < 2*time.Second {
			return
		}
		lastReport = time.Now()
		fmt.Fprintf(os.Stderr, "decide ensure — %s %d/%d MB\n", path, done>>20, total>>20)
	}
	if err := bialy.EnsureModel(ctx, dir, progress); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "decision model ready model=%s revision=%s path=%s\n", model.ID, model.Revision, dir)
	return nil
}
