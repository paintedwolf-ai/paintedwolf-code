// Command opengrep-conformance verifies the shipping rule selection against isolated source cases.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/scan/rules"
)

func main() {
	os.Exit(cli(os.Args[1:], os.Stderr))
}

func cli(argv []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("opengrep-conformance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	binary := flags.String("opengrep", "opengrep", "Pinned scanner executable")
	language := flags.String("language", "", "One corpus language (empty runs all)")
	corpus := flags.String("corpus", shippingCorpus, "Corpus directory relative to the module root; a directory other than the shipping corpus is checked for references only")
	projects := flags.String("projects", "", "Adjudicated project corpus for differential evaluation")
	candidateRules := flags.String("rules", "", "Candidate rule YAML for project evaluation (empty uses shipping selection)")
	repeats := flags.Int("repeats", 2, "Independent runs per project and analysis mode")
	projectTimeout := flags.Duration("project-timeout", defaultProjectTimeout, "Per-project scan timeout")
	mode := flags.String("mode", "", "Analysis mode (empty uses shipping policy for rule cases and both modes for projects)")
	admit := flags.String("admit", "", "Require held-out admission of this analysis mode")
	if err := flags.Parse(argv); err != nil {
		return 2
	}
	if *projects != "" {
		if err := evaluateProjects(*binary, *projects, *candidateRules, projectEvaluationOptions{Repeats: *repeats, Mode: opengrep.Mode(*mode), Admit: opengrep.Mode(*admit), Timeout: *projectTimeout}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *admit != "" || *candidateRules != "" {
		fmt.Fprintln(stderr, "-admit and -rules require -projects")
		return 1
	}
	if err := run(*binary, *language, *corpus, opengrep.Mode(*mode)); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// shippingCorpus is the corpus that gates the shipped rule selection.
const shippingCorpus = "test/testdata/opengrep"

func run(binary, language, corpus string, mode opengrep.Mode) (err error) {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if !filepath.IsLocal(corpus) {
		return fmt.Errorf("corpus must be a path inside the module: %s", corpus)
	}
	cfg, err := rules.LoadOpengrepGates()
	if err != nil {
		return err
	}
	if mode != "" {
		cfg.Analysis.Mode = mode
		if err := cfg.Analysis.Validate(); err != nil {
			return err
		}
	}
	bundle, err := rules.CompileGateRules(cfg, root)
	if err != nil {
		return err
	}
	suites, err := loadSuites(filepath.Join(root, filepath.FromSlash(corpus)))
	if err != nil {
		return err
	}
	if err := validateCoverage(bundle, suites, corpus == shippingCorpus); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "opengrep-conformance-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	cases, err := materialize(dir, suites, language)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	report, err := scan(ctx, binary, dir, bundle, cfg.Analysis)
	if err != nil {
		return err
	}
	return compare(report, cases)
}
