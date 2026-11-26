package bundleddriver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/rules"
	scansourceview "github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

type OpenGrepScanner struct {
	id              string
	homeDir         string
	engineRoot      string
	manifest        *bundled.Manifest
	jobs            int
	processPriority exec.ProcessPriority
	runtimePolicy   scancatalog.RuntimePolicy
}

type OpenGrepOptions struct {
	ID      string
	HomeDir string
	// EngineRoot binds app resources; empty uses the installed host's root.
	EngineRoot      string
	Manifest        *bundled.Manifest
	Jobs            int
	ProcessPriority exec.ProcessPriority
	RuntimePolicy   scancatalog.RuntimePolicy
}

func NewOpenGrepScanner(opts OpenGrepOptions) *OpenGrepScanner {
	id := opts.ID
	if id == "" {
		id = "lycaon-sast"
	}
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = 2
	}
	prio := opts.ProcessPriority
	if prio == "" {
		prio = exec.ProcessPriorityBelowNormal
	}
	engineRoot := opts.EngineRoot
	if engineRoot == "" {
		engineRoot = configlayout.EngineRoot()
	}
	return &OpenGrepScanner{
		id:              id,
		homeDir:         opts.HomeDir,
		engineRoot:      engineRoot,
		manifest:        opts.Manifest,
		jobs:            jobs,
		processPriority: prio,
		runtimePolicy:   opts.RuntimePolicy.Normalized(),
	}
}

func (o *OpenGrepScanner) ID() string { return o.id }

func (o *OpenGrepScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity}
}

func (o *OpenGrepScanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
	projectDir := strings.TrimSpace(req.ProjectDir)
	if projectDir == "" {
		return nil, fmt.Errorf("project dir required")
	}
	targets := scan.EffectiveScanTargets(projectDir, req.Paths)
	for _, target := range targets {
		if err := scansourceview.AssertScanPathWithinProject(projectDir, target); err != nil {
			return nil, err
		}
	}

	bin, err := bundled.ResolveOpenGrepBinary(o.manifest, o.homeDir, o.engineRoot)
	if err != nil {
		return nil, err
	}

	gates, err := rules.LoadOpengrepGates()
	if err != nil {
		return nil, fmt.Errorf("opengrep gates: %w", err)
	}
	excludes, err := rules.LoadPathExcludes()
	if err != nil {
		return nil, fmt.Errorf("opengrep path excludes: %w", err)
	}

	return o.runWithPolicy(ctx, projectDir, bin, targets, excludes.Patterns(), gates.Analysis, func() (opengrepRunFiles, func(), []string, error) {
		runFiles, cleanup, err := newOpengrepRunFiles()
		if err != nil {
			return opengrepRunFiles{}, nil, nil, err
		}
		// Execution reads the same configuration source the contract hashes.
		rulePaths, err := rules.MaterializeGateRules(gates, "", runFiles.dir())
		if err != nil {
			cleanup()
			return opengrepRunFiles{}, nil, nil, err
		}
		return runFiles, cleanup, rulePaths, nil
	})
}

type runStartKey struct{}

func runStartedAt(ctx context.Context, fallback time.Time) time.Time {
	if started, ok := ctx.Value(runStartKey{}).(time.Time); ok && !started.IsZero() {
		return started
	}
	return fallback
}

func hardLimitTimeoutError(ctx context.Context, started time.Time) *exec.TimeoutError {
	deadline, _ := ctx.Deadline()
	return &exec.TimeoutError{
		Elapsed:         time.Since(started),
		Deadline:        deadline,
		Cause:           context.Cause(ctx),
		CommandDeadline: true,
	}
}

func classifyRunError(ctx context.Context, started time.Time, err error) error {
	if err == nil {
		if errors.Is(context.Cause(ctx), scancatalog.ErrHardLimitExceeded) {
			return fmt.Errorf("opengrep scan: %w", hardLimitTimeoutError(ctx, started))
		}
		if ctx.Err() != nil {
			return fmt.Errorf("opengrep scan interrupted: %w", ctx.Err())
		}
		return nil
	}
	var timeout *exec.TimeoutError
	if errors.As(err, &timeout) && timeout.CommandDeadline {
		return err
	}
	if errors.Is(context.Cause(ctx), scancatalog.ErrHardLimitExceeded) {
		return fmt.Errorf("opengrep scan: %w", hardLimitTimeoutError(ctx, started))
	}
	return err
}

func (o *OpenGrepScanner) runWithPolicy(
	ctx context.Context,
	projectDir, bin string,
	targets, excludes []string,
	analysis opengrep.Analysis,
	prepare func() (opengrepRunFiles, func(), []string, error),
) (*scanoutput.Result, error) {
	started := time.Now()
	ctx = context.WithValue(ctx, runStartKey{}, started)
	ctx, cancel := o.runtimePolicy.Context(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, classifyRunError(ctx, started, fmt.Errorf("opengrep scan interrupted: %w", err))
	}
	runFiles, cleanup, rulePaths, err := prepare()
	if err != nil {
		return nil, classifyRunError(ctx, started, err)
	}
	defer cleanup()
	parsed, err := o.scanTargets(ctx, projectDir, bin, rulePaths, targets, excludes, analysis, runFiles)
	if err != nil {
		return nil, classifyRunError(ctx, started, err)
	}
	projectionDir := filepath.Join(runFiles.dir(), "embedded")
	origins, limitations, err := scansourceview.Materialize(ctx, projectDir, projectionDir, parsed.ScannedPaths)
	if err != nil {
		return nil, classifyRunError(ctx, started, fmt.Errorf("embedded scripts: %w", err))
	}
	for _, limitation := range limitations {
		parsed.Warnings = append(parsed.Warnings, api.ScanWarning{Kind: api.ScanWarningFilePartialParse, File: limitation.File, Construct: limitation.Construct, Message: limitation.Detail, StartLine: limitation.Start.Line, StartColumn: limitation.Start.Column})
	}
	if len(origins) > 0 {
		embeddedFiles := runFiles
		embeddedFiles.jsonPath = filepath.Join(runFiles.dir(), "embedded.json")
		extra, err := o.scanTargets(ctx, projectDir, bin, rulePaths, []string{projectionDir}, nil, analysis, embeddedFiles)
		if err != nil {
			return nil, classifyRunError(ctx, started, err)
		}
		if err := scanoutput.RemapOpengrepSources(extra, origins, projectDir); err != nil {
			return nil, classifyRunError(ctx, started, err)
		}
		parsed.Findings = append(parsed.Findings, extra.Findings...)
		parsed.FindingsCount += extra.FindingsCount
		parsed.Warnings = append(parsed.Warnings, extra.Warnings...)
	}
	if err := scanoutput.ValidateOpengrepLocations(parsed, projectDir); err != nil {
		return nil, classifyRunError(ctx, started, err)
	}
	bundle, err := os.ReadFile(rulePaths[0])
	if err != nil {
		return nil, classifyRunError(ctx, started, err)
	}
	if err := filterNonCode(ctx, parsed, projectDir, bundle); err != nil {
		return nil, classifyRunError(ctx, started, err)
	}
	if err := classifyRunError(ctx, started, nil); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (o *OpenGrepScanner) scanTargets(ctx context.Context, projectDir, bin string, rulePaths, targets, excludes []string, analysis opengrep.Analysis, runFiles opengrepRunFiles) (*scanoutput.Result, error) {
	return scanOpengrepTargetBatches(ctx, targets, func(batch []string) (*scanoutput.Result, error) {
		return o.scanTargetBatch(ctx, projectDir, bin, rulePaths, batch, excludes, analysis, runFiles)
	})
}

func (o *OpenGrepScanner) scanTargetBatch(ctx context.Context, projectDir, bin string, rulePaths, targets, excludes []string, analysis opengrep.Analysis, runFiles opengrepRunFiles) (*scanoutput.Result, error) {
	targets, err := opengrepRelativeTargets(projectDir, targets)
	if err != nil {
		return nil, err
	}
	args, err := (opengrep.Invocation{Analysis: analysis, Output: runFiles.jsonPath, Rules: rulePaths, Targets: targets, Jobs: o.jobs, Excludes: excludes}).Args()
	if err != nil {
		return nil, err
	}
	launch, err := opengrepLaunch(projectDir, runFiles)
	if err != nil {
		return nil, err
	}

	if err := os.Remove(runFiles.jsonPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("opengrep clear prior batch output: %w", err)
	}
	batchStarted := time.Now()
	output, console, exitCode, err := exec.RunSeparate(ctx, bin, args, exec.ExecOpts{
		Launch:          launch,
		Dir:             projectDir,
		Timeout:         time.Duration(o.runtimePolicy.HardLimitSec) * time.Second,
		NoTimeout:       o.runtimePolicy.HardLimitSec == 0,
		MaxOutputBytes:  exec.DefaultMaxOutputBytes,
		Env:             opengrepSubprocessEnv(runFiles),
		ProcessPriority: o.processPriority,
	})
	console = append(output, console...)
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), scancatalog.ErrHardLimitExceeded) {
			return nil, fmt.Errorf("opengrep scan (exit %d): %w%s", exitCode, hardLimitTimeoutError(ctx, runStartedAt(ctx, batchStarted)), o.consoleTail(console))
		}
		return nil, fmt.Errorf("opengrep scan interrupted: %w", ctx.Err())
	}
	if err != nil && !errors.Is(err, exec.ErrOutputTruncated) {
		return nil, fmt.Errorf("opengrep scan (exit %d): %w%s", exitCode, err, o.consoleTail(console))
	}

	report, err := openOpenGrepJSONOutput(runFiles.jsonPath)
	if err != nil {
		return nil, fmt.Errorf("%w (exit=%d)%s", err, exitCode, o.consoleTail(console))
	}
	parsed, parseErr := scanoutput.ParseOpengrepJSON(report)
	closeErr := report.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("opengrep parse: %w (report_bytes=%d, exit=%d)%s", parseErr, fileSize(runFiles.jsonPath), exitCode, o.consoleTail(console))
	}
	if closeErr != nil {
		return nil, fmt.Errorf("opengrep close json output: %w", closeErr)
	}
	if err := opengrep.ValidateReportExit(exitCode, parsed.FindingsCount, len(parsed.Warnings)); err != nil {
		return nil, err
	}
	parsed.Categories = o.Categories()
	return parsed, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

// consoleTail screens diagnostic output before applying its limit.
func (o *OpenGrepScanner) consoleTail(console []byte) string {
	return scan.DiagnosticSuffix("console", scan.DiagnosticSubject{
		Stream:     scan.DiagnosticConsole,
		Categories: o.Categories(),
	}, console)
}

// CLI state stays inside the jail output root.
func opengrepSubprocessEnv(files opengrepRunFiles) []string {
	out := append([]string(nil), os.Environ()...)
	pins := map[string]string{
		"XDG_CACHE_HOME":             files.dir(),
		"TMPDIR":                     files.dir(),
		"TMP":                        files.dir(),
		"TEMP":                       files.dir(),
		"LANG":                       "C.UTF-8",
		"LC_ALL":                     "C.UTF-8",
		"LC_CTYPE":                   "C.UTF-8",
		"PYTHONIOENCODING":           "utf-8",
		"PYTHONUTF8":                 "1",
		"SEMGREP_LOG_FILE":           files.logPath,
		"SEMGREP_SETTINGS_FILE":      files.settingsPath,
		"SEMGREP_VERSION_CACHE_PATH": files.versionCachePath,
		"SEMGREP_SEND_METRICS":       "off",
	}
	for k, v := range pins {
		prefix := k + "="
		replaced := false
		for i, kv := range out {
			if strings.HasPrefix(kv, prefix) {
				out[i] = prefix + v
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, prefix+v)
		}
	}
	return out
}

func filterNonCode(ctx context.Context, result *scanoutput.Result, project string, bundle []byte) error {
	coverage, err := rules.CoverageRuleIDs(bundle)
	if err != nil {
		return err
	}
	generic, err := rules.GenericRuleIDs(bundle)
	if err != nil {
		return err
	}
	sourceContexts := make(map[string]*scansourceview.NonCode)
	filtered := result.Findings[:0]
	for _, finding := range result.Findings {
		if coverage[strings.TrimPrefix(finding.RuleID, "opengrep:")] {
			continue
		}
		suppress := false
		if generic[strings.TrimPrefix(finding.RuleID, "opengrep:")] && len(finding.Locations) > 0 {
			loc := finding.Locations[0]
			context := sourceContexts[loc.URI]
			if context == nil {
				context, err = scansourceview.ReadNonCode(ctx, project, loc.URI)
				if err != nil {
					var limitation *scansourceview.Limitation
					if !errors.As(err, &limitation) {
						return err
					}
					result.Warnings = append(result.Warnings, api.ScanWarning{Kind: api.ScanWarningFilePartialParse, File: loc.URI, Construct: limitation.Construct, Message: limitation.Detail})
					context = &scansourceview.NonCode{}
				}
				sourceContexts[loc.URI] = context
			}
			suppress = context.CoversSpan(loc.StartLine, loc.StartColumn, loc.EndLine, loc.EndColumn)
		}
		if !suppress {
			filtered = append(filtered, finding)
		}
	}
	result.Findings = filtered
	result.FindingsCount = len(filtered)
	return nil
}

// opengrepLaunch confines OpenGrep to reading the project and writing its run files.
func opengrepLaunch(projectDir string, runFiles opengrepRunFiles) (exec.LaunchPlan, error) {
	confinement, err := scan.BundledScannerConfinement(projectDir, runFiles.workspace)
	if err != nil {
		return exec.LaunchPlan{}, err
	}
	return exec.BundledScannerLaunch("opengrep", confinement), nil
}
