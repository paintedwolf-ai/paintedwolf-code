// Command commentlint checks repository source comments.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"time"
)

func main() {
	root := flag.String("root", "", "repository root")
	configPath := flag.String("config", "", "JSON config path, relative to the repository root")
	maxLine := flag.Int("max-line-length", 120, "maximum comment line length; zero disables")
	markerIssue := flag.Bool("work-markers-require-issue", true, "require work markers to include an issue")
	issueExpr := flag.String("issue-pattern", `(?:#[0-9]+|[A-Z][A-Z0-9]+-[0-9]+)`, "issue reference regexp")
	localPaths := flag.Bool("local-home-paths", true, "reject the current home directory in comments")
	parseTimeout := flag.Duration("parse-timeout", 30*time.Second, "maximum parse time per file")
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "number of parser workers")
	reportOnly := flag.Bool("report-only", false, "exit successfully when findings exist")
	jsonOutput := flag.Bool("json", false, "write the report as JSON")
	var forbidden stringFlags
	var excludes stringFlags
	flag.Var(&forbidden, "forbid", "named regexp rule as NAME=REGEXP; repeatable")
	flag.Var(&excludes, "exclude", "repo-relative file or directory to exclude; repeatable")
	flag.Parse()

	repoRoot, err := resolveRoot(*root)
	if err != nil {
		fatal(err)
	}
	cfg, err := loadConfig(repoRoot, *configPath)
	if err != nil {
		fatal(err)
	}
	explicit := explicitFlags()
	applyConfig(cfg, explicit, maxLine, markerIssue, issueExpr, localPaths, parseTimeout)
	if *maxLine < 0 {
		fatal(errors.New("max line length cannot be negative"))
	}
	if *parseTimeout <= 0 {
		fatal(errors.New("parse timeout must be positive"))
	}
	if *workers <= 0 {
		fatal(errors.New("workers must be positive"))
	}
	issuePattern, err := regexp.Compile(*issueExpr)
	if err != nil {
		fatal(fmt.Errorf("issue pattern: %w", err))
	}
	forbidden = append(configForbidden(cfg.Forbid), forbidden...)
	forbiddenRules, err := compileForbidden(forbidden)
	if err != nil {
		fatal(err)
	}
	opts := options{
		root: repoRoot, maxLineLength: *maxLine, workMarkersRequireIssue: *markerIssue,
		issuePattern: issuePattern, forbidden: forbiddenRules, excludes: append(cfg.Excludes, excludes...),
		parseTimeout: *parseTimeout, workers: *workers, reportOnly: *reportOnly, json: *jsonOutput,
	}
	if *localPaths {
		opts.localHomePath = homePathPattern()
	}

	targets := flag.Args()
	if len(targets) == 0 {
		targets = []string{"."}
	}
	rep, err := lint(opts, targets)
	if err != nil {
		fatal(err)
	}
	sortFindings(rep.Findings)
	if opts.json {
		if err := json.NewEncoder(os.Stdout).Encode(rep); err != nil {
			fatal(err)
		}
	} else {
		printText(rep)
	}
	if !opts.reportOnly && (len(rep.Findings) > 0 || len(rep.ParseTimeouts) > 0) {
		os.Exit(1)
	}
}

func sortFindings(findings []finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Rule < findings[j].Rule
	})
}

func fatal(err error) {
	if !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "commentlint:", err)
	}
	os.Exit(2)
}
