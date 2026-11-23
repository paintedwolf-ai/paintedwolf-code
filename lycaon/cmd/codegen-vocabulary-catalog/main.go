// Command codegen-vocabulary-catalog regenerates the committed workflow
// vocabulary catalog snapshot at schemas/workflow_vocabulary.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/vocabulary"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fatal(err)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("codegen-vocabulary-catalog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	out := flags.String("out", "", "output path for schemas/workflow_vocabulary.json")
	check := flags.Bool("check", false, "exit 1 if the output is stale")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	body := render()
	if *check {
		// #nosec G304 -- output paths are explicit command inputs.
		current, err := os.ReadFile(*out)
		if err != nil || string(current) != body {
			return fmt.Errorf("%s is stale — run ./task codegen:vocabulary-catalog", *out)
		}
		return nil
	}
	if err := os.WriteFile(*out, []byte(body), 0o600); err != nil {
		return err
	}
	return nil
}

func render() string {
	doc := map[string]any{
		"$schema": "./workflow_vocabulary.schema.json",
		"entries": vocabulary.ExportCatalog(),
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal(err)
	}
	return string(b) + "\n"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
