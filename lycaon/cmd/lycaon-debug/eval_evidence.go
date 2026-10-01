package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/eval/episode"
)

func runEvalEvidence(args []string) error {
	flags := flag.NewFlagSet("eval evidence", flag.ContinueOnError)
	capture := flags.String("capture", "", "closed application capture directory")
	project := flags.String("project", "", "retained delivered project directory")
	sessionID := flags.String("session", "", "root session id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *capture == "" || *sessionID == "" || flags.NArg() != 0 {
		return fmt.Errorf("eval evidence requires --capture and --session")
	}
	result, err := episode.Read(context.Background(), *capture, *sessionID)
	if err != nil {
		return err
	}
	if err := result.ReadSources(context.Background(), *capture, *project); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
