// Command opengrep-conformance verifies the shipping rule selection against isolated source cases.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/runnerbudget"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/scan/rules"
)

func main() {
	binary := flag.String("opengrep", "opengrep", "Pinned scanner executable")
	language := flag.String("language", "", "One corpus language (empty runs all)")
	corpus := flag.String("corpus", shippingCorpus, "Corpus directory relative to the module root; a directory other than the shipping corpus is checked for references only")
	projects := flag.String("projects", "", "Adjudicated project corpus for differential evaluation")
	candidateRules := flag.String("rules", "", "Candidate rule YAML for project evaluation (empty uses shipping selection)")
	repeats := flag.Int("repeats", 2, "Independent runs per project and analysis mode")
	projectTimeout := flag.Duration("project-timeout", defaultProjectTimeout, "Per-project base timeout before the contention scale is applied")
	mode := flag.String("mode", "", "Analysis mode (empty uses shipping policy for rule cases and both modes for projects)")
	admit := flag.String("admit", "", "Require held-out admission of this analysis mode")
	flag.Parse()
	if *projects != "" {
		if err := evaluateProjects(*binary, *projects, *candidateRules, projectEvaluationOptions{Repeats: *repeats, Mode: opengrep.Mode(*mode), Admit: opengrep.Mode(*admit), Timeout: *projectTimeout}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *admit != "" || *candidateRules != "" {
		fmt.Fprintln(os.Stderr, "-admit and -rules require -projects")
		os.Exit(1)
	}
	if err := run(*binary, *language, *corpus, opengrep.Mode(*mode)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute*time.Duration(runnerbudget.TimeoutScale()))
	defer cancel()
	report, err := scan(ctx, binary, dir, bundle, cfg.Analysis)
	if err != nil {
		return err
	}
	return compare(report, cases)
}
