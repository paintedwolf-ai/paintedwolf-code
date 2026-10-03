package external

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	scansourceview "github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

// Scanner runs a user-installed CLI via fixed argv (no shell).
type Scanner struct {
	entry           scancatalog.ScannerEntry
	moduleRoot      string
	processPriority exec.ProcessPriority
	// mapParser is bound at construction for map/json entries.
	mapParser scanoutput.OutputParser
}

// NewScanner constructs an external driver for one catalog entry at the given
// process priority. configDir resolves map/json device mappers; empty uses
// configdir.UserConfigDir. Empty prio defaults to below-normal.
func NewScanner(entry scancatalog.ScannerEntry, moduleRoot, configDir string, prio exec.ProcessPriority) (*Scanner, error) {
	if prio == "" {
		prio = exec.ProcessPriorityBelowNormal
	}
	s := &Scanner{entry: entry, moduleRoot: moduleRoot, processPriority: prio}
	if strings.TrimSpace(entry.OutputParser) == scanoutput.OutputParserMapJSON {
		if strings.TrimSpace(configDir) == "" {
			var err error
			configDir, err = configdir.UserConfigDir()
			if err != nil {
				return nil, err
			}
		}
		mapper, err := scanoutput.LoadMapper(configDir, entry.MapperID)
		if err != nil {
			return nil, err
		}
		s.mapParser = scanoutput.NewMapJSONParser(mapper)
	}
	return s, nil
}

func (s *Scanner) ID() string { return s.entry.ID }

func (s *Scanner) Categories() []api.ScanCategory { return s.entry.CategoriesAPI() }

func (s *Scanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
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
	if len(req.Paths) > 0 && !scancatalog.CommandUsesScanTarget(s.entry.Command) {
		return nil, fmt.Errorf("%s: path-scoped scan requires command token %s", s.entry.ID, scancatalog.ArgTokenScanTarget)
	}

	combined := &scanoutput.Result{Categories: s.Categories()}
	for _, target := range targets {
		result, err := s.runTarget(ctx, projectDir, target)
		if err != nil {
			return nil, err
		}
		combined.Findings = append(combined.Findings, result.Findings...)
		combined.Warnings = append(combined.Warnings, result.Warnings...)
	}
	combined.FindingsCount = len(combined.Findings)
	return combined, nil
}

func (s *Scanner) runTarget(ctx context.Context, projectDir, target string) (*scanoutput.Result, error) {
	// File-report scanners receive a host-managed temporary path.
	_, reportPath, cleanupReport, err := s.newRunFiles()
	if err != nil {
		return nil, err
	}
	defer cleanupReport()

	argv, err := scancatalog.ExpandCommand(s.entry.Command, projectDir, target, reportPath)
	if err != nil {
		return nil, err
	}
	binary, err := scancatalog.ResolveBinaryOutsideRoots(argv[0], []string{projectDir})
	if err != nil {
		return nil, err
	}
	args := argv[1:]

	workdir := projectDir
	switch s.entry.WorkdirOrDefault() {
	case scancatalog.WorkdirProject:
		workdir = projectDir
	case scancatalog.WorkdirModuleRoot:
		workdir = s.moduleRoot
	}

	timeout := time.Duration(s.entry.RuntimePolicy().HardLimitSec) * time.Second
	// Scanners write progress and warnings to stderr and the report to stdout;
	// a merged buffer would not parse.
	out, errOut, exitCode, err := exec.RunSeparate(ctx, binary, args, exec.ExecOpts{
		Launch:          exec.ExternalScannerLaunch(s.entry.ID),
		Dir:             workdir,
		Timeout:         timeout,
		NoTimeout:       s.entry.RuntimePolicy().HardLimitSec == 0,
		MaxOutputBytes:  exec.DefaultMaxScanOutputBytes,
		Env:             scannerEnvironment(exec.InheritedEnviron(), s.entry.Env),
		ProcessPriority: s.processPriority,
	})
	if err != nil && exitCode < 0 {
		return nil, fmt.Errorf("%s: %w%s", s.entry.ID, err, s.stderrSuffix(errOut))
	}
	if reportPath != "" {
		report, readErr := readReportFile(reportPath, int64(exec.DefaultMaxScanOutputBytes))
		if readErr != nil {
			return nil, fmt.Errorf("%s: exit %d wrote no report to %s: %w%s",
				s.entry.ID, exitCode, reportPath, readErr, s.stderrSuffix(errOut))
		}
		out = report
	}

	parserID := strings.TrimSpace(s.entry.OutputParser)
	if !s.entry.ExitCodeAllowed(exitCode) {
		return nil, fmt.Errorf("%s: exit %d is outside the declared success codes %v; "+
			"the report it produced cannot be trusted to mean the project is clean: %s%s",
			s.entry.ID, exitCode, s.entry.OkExitCodes, s.reportExcerpt(out), s.stderrSuffix(errOut))
	}

	var parsed *scanoutput.Result
	var perr error
	if s.mapParser != nil {
		parsed, perr = s.mapParser.Parse(out)
	} else {
		parsed, perr = scanoutput.ParseOutput(parserID, out) //nolint:contextcheck // ParseOutput is pure JSON decode
	}
	if perr != nil {
		if exitCode != 0 {
			return nil, fmt.Errorf("%s: exit %d parse failed: %w; output: %s%s",
				s.entry.ID, exitCode, perr, s.reportExcerpt(out), s.stderrSuffix(errOut))
		}
		return nil, fmt.Errorf("%s: parse: %w%s", s.entry.ID, perr, s.stderrSuffix(errOut))
	}
	prefixRuleIDs(s.entry.ID, parsed)
	classifyExternalFindings(parsed, s.Categories())
	return parsed, nil
}

// newRunFiles isolates report capture from the project.
func (s *Scanner) newRunFiles() (dir, reportPath string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "paintedwolf-scan-")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("%s: create run directory: %w", s.entry.ID, err)
	}
	// The reaper removes the run directory if the host exits before this cleanup.
	cleanup = exec.TrackScratchDir(dir)
	if !scancatalog.CommandWantsReportFile(s.entry.Command) {
		return dir, "", cleanup, nil
	}
	f, err := os.CreateTemp(dir, "report-*.json")
	if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("%s: create report file: %w", s.entry.ID, err)
	}
	reportPath = filepath.Clean(f.Name())
	if err := f.Close(); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("%s: close report file: %w", s.entry.ID, err)
	}
	return dir, reportPath, cleanup, nil
}

// stderrSuffix projects the scanner's console output for an error message.
func (s *Scanner) stderrSuffix(errOut []byte) string {
	return scan.DiagnosticSuffix("stderr", s.diagnosticSubject(scan.DiagnosticConsole), errOut)
}

// reportExcerpt projects the scanner's findings document for an error message.
func (s *Scanner) reportExcerpt(out []byte) string {
	return scan.DiagnosticExcerpt(s.diagnosticSubject(scan.DiagnosticReport), out)
}

func (s *Scanner) diagnosticSubject(stream scan.DiagnosticStream) scan.DiagnosticSubject {
	return scan.DiagnosticSubject{
		Stream:     stream,
		ScopeKind:  scancatalog.ScopeKind(strings.TrimSpace(s.entry.ScopeKind)),
		Categories: s.Categories(),
	}
}

func readReportFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("report is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("report exceeds %d byte limit", maxBytes)
	}
	file, err := os.Open(path) // #nosec G304 -- path is created inside the host-managed run directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("report exceeds %d byte limit", maxBytes)
	}
	return raw, nil
}

func prefixRuleIDs(scannerID string, result *scanoutput.Result) {
	if result == nil {
		return
	}
	prefix := scannerID + ":"
	for i := range result.Findings {
		rid := strings.TrimSpace(result.Findings[i].RuleID)
		if rid == "" {
			result.Findings[i].RuleID = prefix + "finding"
		} else if !strings.HasPrefix(rid, prefix) {
			result.Findings[i].RuleID = prefix + rid
		}
		scanfindings.RefreshFingerprint(&result.Findings[i])
	}
}

func classifyExternalFindings(result *scanoutput.Result, categories []api.ScanCategory) {
	if result == nil {
		return
	}
	kind := externalFindingKind(categories)
	for i := range result.Findings {
		finding := &result.Findings[i]
		if finding.Properties == nil {
			finding.Properties = &api.SecurityFindingProperties{}
		}
		if finding.Properties.Lycaon == nil {
			finding.Properties.Lycaon = &api.SecurityFindingLycaonProperties{}
		}
		if kind != api.FindingKindCustom {
			finding.Properties.Lycaon.Kind = kind
		}
		finding.Properties.Lycaon.Categories = append([]api.ScanCategory(nil), categories...)
		scanfindings.RefreshFingerprint(finding)
	}
	result.Categories = append([]api.ScanCategory(nil), categories...)
}

func externalFindingKind(categories []api.ScanCategory) api.FindingKind {
	switch scancatalog.PrimaryCategory(apiCategoriesToStrings(categories)) {
	case string(api.ScanCategorySAST):
		return api.FindingKindSAST
	case string(api.ScanCategorySecret):
		return api.FindingKindSecret
	case string(api.ScanCategorySCA):
		return api.FindingKindSCA
	case string(api.ScanCategoryContainer):
		return api.FindingKindContainer
	default:
		return api.FindingKindCustom
	}
}

func apiCategoriesToStrings(categories []api.ScanCategory) []string {
	out := make([]string, len(categories))
	for i, category := range categories {
		out[i] = string(category)
	}
	return out
}

// scannerBaselineEnvironment is the inherited scanner environment: fixed
// non-egress keys plus confine's proxy keys.
var scannerBaselineEnvironment = buildScannerBaselineEnvironment()

func buildScannerBaselineEnvironment() map[string]struct{} {
	env := map[string]struct{}{
		"HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {}, "TERM": {},
		"LANG": {}, "LC_ALL": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {}, "PATH": {},
		"XDG_CONFIG_HOME": {}, "XDG_CACHE_HOME": {}, "XDG_DATA_HOME": {},
		"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
	}
	for _, key := range confine.ProxyEnvKeys() {
		env[key] = struct{}{}
	}
	return env
}

func scannerEnvironment(base []string, names []string) []string {
	want := make(map[string]struct{}, len(scannerBaselineEnvironment)+len(names))
	for name := range scannerBaselineEnvironment {
		want[name] = struct{}{}
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			want[n] = struct{}{}
		}
	}
	var out []string
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		_, keep := want[key]
		if keep || strings.HasPrefix(key, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}
