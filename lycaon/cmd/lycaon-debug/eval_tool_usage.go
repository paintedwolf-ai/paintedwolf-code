package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/eval/toolusage"
	"github.com/lycaon/lycaon/internal/harnessfixture"
)

func runEvalToolUsage(args []string) error {
	flags, err := parseEvalToolUsageFlags(args)
	if err != nil {
		return err
	}
	if flags.releaseResources != "" {
		return harnessfixture.ReleaseWriteResources(flags.releaseResources)
	}
	if flags.refresh != "" {
		return toolusage.RefreshSuiteReport(flags.refresh)
	}
	if flags.compare != "" {
		baseline, err := toolusage.LoadSuiteReport(flags.compare)
		if err != nil {
			return err
		}
		candidate, err := toolusage.LoadSuiteReport(flags.from)
		if err != nil {
			return err
		}
		return toolusage.CompareSuites(os.Stdout, baseline, candidate)
	}
	if flags.suite != "" {
		return runEvalSuite(flags)
	}

	var profile toolusage.Profile
	if flags.from != "" {
		profile, err = toolusage.ProfileFromCaptureDir(flags.from)
		if err != nil {
			return err
		}
	} else {
		if !flags.allowLive {
			return fmt.Errorf("live evaluation spends real tokens; pass --allow-live (replay with --from is free)")
		}
		if flags.addr == "" {
			return fmt.Errorf("no sidecar address: pass --addr or set LYCAON_E2E_ADDR (harness ports are per-process — see the ./task den:harness banner)")
		}
		corpusPath, err := toolusage.ResolveModulePath(flags.corpus)
		if err != nil {
			return err
		}
		corpus, err := toolusage.LoadCorpus(corpusPath)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		profile, err = toolusage.RunLive(ctx, toolusage.LiveOptions{
			AllowLive:  flags.allowLive,
			BaseURL:    flags.addr,
			Token:      flags.token,
			ProjectDir: flags.project,
			Corpus:     corpus,
			Runs:       flags.runs,
			CaptureDir: flags.capture,
			Timeout:    flags.timeout,
		})
		if err != nil {
			return err
		}
	}

	if flags.baseline != "" {
		basePath, err := toolusage.ResolveModulePath(flags.baseline)
		if err != nil {
			return err
		}
		b, err := loadBaseline(basePath)
		if err != nil {
			return err
		}
		if !toolusage.CompareProfiles(b, profile) {
			return fmt.Errorf("profile differs from baseline %s: %s", flags.baseline, toolusage.DiffSummary(b, profile))
		}
	}

	if flags.out != "" {
		f, err := os.Create(flags.out) // #nosec G703 -- operator-selected output path
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if err := profile.EncodeJSON(f); err != nil {
			return err
		}
	}

	if flags.table {
		profile.RenderTable(os.Stdout)
	} else if flags.json || !isTTY() {
		if err := profile.EncodeJSON(os.Stdout); err != nil {
			return err
		}
	} else {
		profile.RenderTable(os.Stdout)
	}
	return nil
}

type evalToolUsageFlags struct {
	releaseResources string
	stateDB          string
	waitForInput     bool
	refresh          string
	compare          string
	allowLive        bool
	suite            string
	cases            string
	expectModel      string
	label            string
	from             string
	addr             string
	token            string
	corpus           string
	project          string
	runs             int
	timeout          time.Duration
	capture          string
	json             bool
	table            bool
	out              string
	baseline         string
}

func parseEvalToolUsageFlags(args []string) (evalToolUsageFlags, error) {
	var f evalToolUsageFlags
	flags := flag.NewFlagSet("eval tool-usage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&f.stateDB, "state-db", "", "read-only application failure ledger for an isolated suite")
	flags.StringVar(&f.releaseResources, "release-resources", "", "release retained external fixture outputs")
	flags.StringVar(&f.refresh, "refresh", "", "refresh a suite report from settled captures without model calls")
	flags.StringVar(&f.compare, "compare", "", "compare baseline suite report against --from candidate report, without model calls")
	flags.BoolVar(&f.allowLive, "allow-live", false, "explicitly authorize paid model calls")
	flags.BoolVar(&f.waitForInput, "wait-for-input", false, "wait for normal operator approvals and answers instead of recording a blocked case")
	flags.StringVar(&f.suite, "suite", "", "outcome evaluation suite YAML")
	flags.StringVar(&f.cases, "cases", "", "suite case ids, comma-separated (default all)")
	flags.StringVar(&f.expectModel, "expect-model", "", "exact expected surfaced coordinator model")
	flags.StringVar(&f.label, "label", "", "comparison arm label")
	flags.StringVar(&f.from, "from", "", "replay capture directory")
	flags.StringVar(&f.addr, "addr", toolusage.DefaultLiveBaseURL(), "sidecar URL")
	flags.StringVar(&f.token, "token", "", "API bearer token")
	flags.StringVar(&f.corpus, "corpus", toolusage.DefaultCorpusPath, "task corpus path")
	flags.StringVar(&f.project, "project", "", "project directory")
	flags.IntVar(&f.runs, "runs", toolusage.DefaultRuns, "corpus repetitions")
	flags.DurationVar(&f.timeout, "timeout", toolusage.DefaultLiveTimeout, "optional per-task deadline (0 disables)")
	flags.StringVar(&f.capture, "capture", "", "capture directory")
	flags.BoolVar(&f.json, "json", false, "JSON output")
	flags.BoolVar(&f.table, "table", false, "table output")
	flags.StringVar(&f.out, "out", "", "profile output path")
	flags.StringVar(&f.baseline, "baseline", "", "baseline comparison path")
	if err := flags.Parse(args); err != nil {
		return f, fmt.Errorf("%w\n%s", err, evalUsage)
	}
	if flags.NArg() != 0 {
		return f, fmt.Errorf("unexpected arguments: %v\n%s", flags.Args(), evalUsage)
	}
	if f.runs < 1 {
		return f, fmt.Errorf("--runs must be a positive integer")
	}
	if f.timeout < 0 {
		return f, fmt.Errorf("--timeout cannot be negative")
	}
	if f.suite != "" && (f.from != "" || f.baseline != "" || f.project != "") {
		return f, fmt.Errorf("--suite owns fresh projects and cannot combine with --from, --baseline, or --project")
	}
	if f.compare != "" && (f.from == "" || f.suite != "" || f.allowLive) {
		return f, fmt.Errorf("--compare requires --from and cannot combine with --suite or --allow-live")
	}
	if f.releaseResources != "" && (f.refresh != "" || f.compare != "" || f.from != "" || f.suite != "" || f.allowLive || f.out != "") {
		return f, fmt.Errorf("--release-resources cannot combine with other modes")
	}
	if f.refresh != "" && (f.compare != "" || f.from != "" || f.suite != "" || f.allowLive || f.out != "") {
		return f, fmt.Errorf("--refresh updates the selected report offline and cannot combine with other modes or --out")
	}
	if f.waitForInput && (f.suite == "" || !f.allowLive) {
		return f, fmt.Errorf("--wait-for-input requires an explicitly enabled live suite")
	}
	return f, nil
}

func loadBaseline(path string) (toolusage.Profile, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-selected baseline path
	if err != nil {
		return toolusage.Profile{}, err
	}
	var p toolusage.Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return toolusage.Profile{}, err
	}
	return p, nil
}
