package main

import (
	"context"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

const placementsUsage = `usage: pw placements {list|purge} [--yes]

  list    show worker branch folders kept on other volumes
  purge   remove them; run with --yes before deleting the configuration folder

A folder on a volume that is not connected is reported and left in place.`

func runPlacements(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", placementsUsage)
	}
	stateRoot, err := configdir.UserConfigDir()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("%s", placementsUsage)
		}
		return listPlacements(stateRoot, out)
	case "purge":
		if len(args) != 2 || args[1] != "--yes" {
			return fmt.Errorf("%s", placementsUsage)
		}
		return purgePlacements(ctx, stateRoot, out)
	default:
		return fmt.Errorf("%s", placementsUsage)
	}
}

func listPlacements(stateRoot string, out io.Writer) error {
	registry, err := workerplacement.Load(stateRoot)
	if err != nil {
		return err
	}
	for _, folder := range registry.Folders {
		state := "available"
		if !workerplacement.Available(folder) {
			state = "unavailable"
		}
		fmt.Fprintf(out, "%s\t%s\n", state, folder.Path)
	}
	return nil
}

func purgePlacements(ctx context.Context, stateRoot string, out io.Writer) error {
	report, err := workerplacement.Purge(ctx, stateRoot)
	for _, path := range report.Removed {
		fmt.Fprintf(out, "removed\t%s\n", path)
	}
	for _, path := range report.Unavailable {
		fmt.Fprintf(out, "unavailable\t%s\n", path)
	}
	return err
}
