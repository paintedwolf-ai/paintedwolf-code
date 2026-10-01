package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/eval/episode"
)

func runEvalCost(args []string) error {
	flags := flag.NewFlagSet("eval cost", flag.ContinueOnError)
	capture := flags.String("capture", "", "closed application capture directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *capture == "" || flags.NArg() != 0 {
		return fmt.Errorf("eval cost requires --capture")
	}
	result, err := episode.ReadCost(context.Background(), *capture)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
