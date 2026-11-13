package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"

	"github.com/lycaon/lycaon/internal/eval/episode"
	"github.com/lycaon/lycaon/internal/fseffect"
)

func runEvalTrainingContract(args []string) error {
	flags := flag.NewFlagSet("eval training-contract", flag.ContinueOnError)
	out := flags.String("out", "", "new directory for contract.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" || flags.NArg() != 0 {
		return errors.New("eval training-contract requires --out")
	}
	if err := os.Mkdir(*out, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(episode.Contract(), "", "  ")
	if err != nil {
		return err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: *out, Rel: "contract.json"}, Source: bytes.NewReader(append(raw, '\n')), Mode: 0o600}); err != nil {
		return err
	}
	return nil
}

func runEvalTrainingBundle(args []string) error {
	flags := flag.NewFlagSet("eval training-bundle", flag.ContinueOnError)
	var req episode.BundleRequest
	flags.StringVar(&req.Capture, "capture", "", "closed application capture directory")
	flags.StringVar(&req.Captures, "captures", "", "training capture directory of that application")
	flags.StringVar(&req.Project, "project", "", "retained delivered project directory")
	flags.StringVar(&req.SessionID, "session", "", "root session id")
	flags.StringVar(&req.Out, "out", "", "new directory for the bundle")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if req.Capture == "" || req.Captures == "" || req.SessionID == "" || req.Out == "" || flags.NArg() != 0 {
		return errors.New("eval training-bundle requires --capture, --captures, --session and --out")
	}
	manifest, err := episode.WriteBundle(context.Background(), req)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(manifest)
}
