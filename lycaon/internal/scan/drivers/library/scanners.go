package library

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fatih/semgroup"
	"github.com/google/osv-scalibr"
	cpb "github.com/google/osv-scalibr/binary/proto/config_go_proto"
	scalibrfs "github.com/google/osv-scalibr/fs"
	"github.com/google/osv-scalibr/inventory"
	"github.com/google/osv-scalibr/plugin"
	pluginconfig "github.com/google/osv-scalibr/plugin/config"
	"github.com/google/osv-scalibr/plugin/list"
	scalibrresult "github.com/google/osv-scalibr/result"
	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/advisory/severity"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	scansourceview "github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
	gconfig "github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/report"
	"github.com/zricethezav/gitleaks/v8/sources"
)

// Library scanner implementation names.
const (
	ImplOSVScalibr = "osv_scalibr"
	ImplGitleaks   = "gitleaks"
)

// ScalibrScanner reports known vulnerabilities in project dependencies.
type ScalibrScanner struct {
	id string
}

// NewScalibrScanner returns a dependency scanner with driver id id.
func NewScalibrScanner(id string) *ScalibrScanner {
	if id == "" {
		id = "lycaon-sca"
	}
	return &ScalibrScanner{id: id}
}

func (s *ScalibrScanner) ID() string { return s.id }

func (s *ScalibrScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity}
}

func (s *ScalibrScanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
	projectDir := strings.TrimSpace(req.ProjectDir)
	if projectDir == "" {
		return nil, fmt.Errorf("project dir required")
	}
	if err := scansourceview.AssertScanPathWithinProject(projectDir, projectDir); err != nil {
		return nil, err
	}

	cacheDir, err := project.EnsureOSVCacheDir("")
	if err != nil {
		return nil, err
	}
	if _, err := severity.Default(); err != nil {
		return nil, err
	}
	pluginCfg := &pluginconfig.PluginConfig{
		ProtoConfig: &cpb.PluginConfig{
			PluginSpecific: []*cpb.PluginSpecificConfig{
				{
					Config: &cpb.PluginSpecificConfig_Osvlocal{
						Osvlocal: &cpb.OSVLocalConfig{
							LocalPath: cacheDir,
							Download:  true,
							RemoteHost: egressclass.RequireSingleFixedEndpoint(
								egressclass.OSVAdvisoryDownload,
								egressclass.LibraryDownload,
							),
						},
					},
				},
			},
		},
	}

	plugins, err := list.FromNames([]string{"default", "vulnmatch/osvlocal"}, pluginCfg)
	if err != nil {
		return nil, fmt.Errorf("scalibr plugins: %w", err)
	}

	capabilities := scalibrCapabilities()
	plugins = plugin.FilterByCapabilities(plugins, capabilities)

	cfg := &scalibr.ScanConfig{
		Plugins:      plugins,
		ScanRoots:    scalibrfs.RealFSScanRoots(projectDir),
		UseGitignore: true,
		Capabilities: capabilities,
		DirsToSkip: []string{
			filepath.Join(projectDir, settingsoverlay.DirName()),
		},
	}
	if len(req.Paths) > 0 {
		cfg.PathsToExtract = scan.EffectiveScanTargets(projectDir, req.Paths)
		for _, target := range cfg.PathsToExtract {
			if err := scansourceview.AssertScanPathWithinProject(projectDir, target); err != nil {
				return nil, err
			}
		}
	}
	if err := cfg.EnableRequiredPlugins(); err != nil {
		return nil, fmt.Errorf("scalibr enable plugins: %w", err)
	}

	sr := scalibr.New().Scan(ctx, cfg)
	if err := validateScalibrResult(sr); err != nil {
		return nil, err
	}
	findings := mapPackageVulns(sr.Inventory.PackageVulns, projectDir, s.id)
	return &scanoutput.Result{
		FindingsCount: len(findings),
		Categories:    s.Categories(),
		Findings:      findings,
	}, nil
}

func validateScalibrResult(result *scalibrresult.ScanResult) error {
	if result == nil || result.Status == nil {
		return fmt.Errorf("scalibr scan returned no status")
	}
	if result.Status.Status != plugin.ScanStatusSucceeded {
		return fmt.Errorf("scalibr scan did not complete: status=%d reason=%s",
			result.Status.Status, strings.TrimSpace(result.Status.FailureReason))
	}
	for _, state := range result.PluginStatus {
		if state == nil || state.Status == nil || state.Status.Status != plugin.ScanStatusSucceeded {
			name := "unknown"
			status := plugin.ScanStatusUnspecified
			reason := ""
			if state != nil {
				name = state.Name
				if state.Status != nil {
					status = state.Status.Status
					reason = state.Status.FailureReason
				}
			}
			return fmt.Errorf("scalibr plugin %q did not complete: status=%d reason=%s",
				name, status, strings.TrimSpace(reason))
		}
	}
	return nil
}

// mapPackageVulns turns matched records into findings, then folds the records
// that describe one vulnerability in one package (an ecosystem database record
// and its GHSA twin both match) into a single row keyed by the canonical id.
func mapPackageVulns(vulns []*inventory.PackageVuln, projectDir, driverID string) []api.SecurityFinding {
	out := make([]api.SecurityFinding, 0, len(vulns))
	categories := []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity}
	for _, pv := range vulns {
		if pv == nil || pv.Vulnerability == nil || strings.TrimSpace(pv.Vulnerability.Id) == "" {
			continue
		}
		adv, level := packageAdvisory(pv)
		msg := adv.OSVID
		if pv.Vulnerability.Summary != "" {
			msg = pv.Vulnerability.Summary
		}
		var locations []api.SecurityFindingLocation
		if pv.Package != nil {
			if path := pv.Package.Location.PathOrEmpty(); path != "" {
				uri := path
				if rel, err := filepath.Rel(projectDir, uri); err == nil {
					uri = rel
				}
				locations = append(locations, api.SecurityFindingLocation{URI: uri})
			}
			for _, loc := range pv.Package.Location.Related {
				if loc.File != nil && loc.File.Path != "" {
					uri := loc.File.Path
					if rel, err := filepath.Rel(projectDir, uri); err == nil {
						uri = rel
					}
					locations = append(locations, api.SecurityFindingLocation{URI: uri})
				}
			}
		}
		out = append(out, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID:   driverID,
			ToolName:   "osv-scalibr",
			RuleID:     advisory.RuleID(adv),
			Level:      level,
			Message:    msg,
			Kind:       api.FindingKindSCA,
			Locations:  locations,
			Advisory:   adv,
			Categories: categories,
		}))
	}
	return scanfindings.MergeByAdvisory(out)
}

func scalibrCapabilities() *plugin.Capabilities {
	osType := plugin.OSUnknown
	switch runtime.GOOS {
	case "linux":
		osType = plugin.OSLinux
	case "darwin":
		osType = plugin.OSMac
	case "windows":
		osType = plugin.OSWindows
	}
	return &plugin.Capabilities{
		OS:            osType,
		Network:       plugin.NetworkOnline,
		DirectFS:      true,
		RunningSystem: true,
	}
}

// GitleaksScanner reports secrets at rest using the scanner catalog profile.
type GitleaksScanner struct {
	fingerprinter *secretmatch.Fingerprinter
	id            string
	jobs          int
	config        gconfig.Config
	profile       *secretmatch.ScannerProfile
}

// GitleaksOptions configures a GitleaksScanner; zero fields use defaults.
type GitleaksOptions struct {
	Fingerprinter *secretmatch.Fingerprinter
	ID            string
	Jobs          int
}

// NewGitleaksScanner compiles the bundled scanner profile.
func NewGitleaksScanner(opts GitleaksOptions) (*GitleaksScanner, error) {
	profile, err := secretmatch.BuildScannerProfile(secretmatch.Bundled())
	if err != nil {
		return nil, fmt.Errorf("gitleaks catalog: %w", err)
	}
	cfg := profile.Config()
	if err := applyGitleaksLycaonExclude(&cfg); err != nil {
		return nil, fmt.Errorf("gitleaks allowlist: %w", err)
	}
	id := opts.ID
	if id == "" {
		id = "lycaon-secrets"
	}
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = 2
	}
	return &GitleaksScanner{id: id, jobs: jobs, config: cfg, profile: profile, fingerprinter: opts.Fingerprinter}, nil
}

func (g *GitleaksScanner) ID() string { return g.id }

func (g *GitleaksScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySecret, api.ScanCategorySecurity}
}

func (g *GitleaksScanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
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

	detector := detect.NewDetector(g.config)
	sem := semgroup.NewGroup(ctx, int64(g.jobs))
	detector.Sema = sem
	var raw []report.Finding
	var warnings []api.ScanWarning
	for _, target := range targets {
		src := &sources.Files{Path: target, Sema: sem, Config: &detector.Config}
		fileCtx, stop := context.WithCancel(ctx)
		if req.FileTimeout > 0 {
			fileCtx, stop = context.WithTimeout(ctx, req.FileTimeout)
		}
		found, err := detector.DetectSource(fileCtx, src)
		stop()
		if err != nil {
			// One file over its budget is a named gap, not a stalled run.
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				warnings = append(warnings, api.ScanWarning{
					Kind: api.ScanWarningTargetUnscanned, File: target,
					Message: fmt.Sprintf("file exceeded the %s per-file time budget", req.FileTimeout),
				})
				continue
			}
			return nil, fmt.Errorf("gitleaks scan %q: %w", target, err)
		}
		raw = append(raw, found...)
	}

	raw = filterGitleaksFindings(g.profile, raw)
	findings := make([]api.SecurityFinding, 0, len(raw))
	var identities []scanoutput.SecretIdentity
	secretCats := []api.ScanCategory{api.ScanCategorySecret, api.ScanCategorySecurity}
	for i := range raw {
		f := &raw[i]
		secret := f.Secret
		if secret == "" {
			secret = f.Match
		}
		ruleID := secretmatch.CatalogRuleID(f.RuleID)
		if g.fingerprinter != nil && secret != "" {
			identities = append(identities, scanoutput.SecretIdentity{FindingIndex: len(findings), ValueFingerprint: string(g.fingerprinter.Fingerprint(secret))})
		}
		findings = append(findings, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID:   g.id,
			ToolName:   "Gitleaks",
			RuleID:     ruleID,
			Level:      api.FindingLevelHigh,
			Message:    f.Description,
			Kind:       api.FindingKindSecret,
			Locations:  []api.SecurityFindingLocation{{URI: f.File, StartLine: f.StartLine}},
			Categories: secretCats,
		}))
		clearGitleaksFinding(f)
	}
	return &scanoutput.Result{
		SecretIdentities: identities,
		FindingsCount:    len(findings),
		Categories:       g.Categories(),
		Findings:         findings,
		Warnings:         warnings,
	}, nil
}

type gitleaksFindingKey struct {
	file       string
	line       int
	lineOffset int
	secret     [sha256.Size]byte
}

func filterGitleaksFindings(profile *secretmatch.ScannerProfile, raw []report.Finding) []report.Finding {
	out := make([]report.Finding, 0, len(raw))
	bySpan := make(map[gitleaksFindingKey]int, len(raw))
	for i := range raw {
		secret := raw[i].Secret
		if secret == "" {
			secret = raw[i].Match
		}
		if !profile.Accept(raw[i].RuleID, secret) || secretmatch.WithinReferenceTokens(raw[i].Line, secret) {
			clearGitleaksFinding(&raw[i])
			continue
		}
		key := gitleaksFindingKey{
			file: raw[i].File, line: raw[i].StartLine,
			lineOffset: strings.Index(raw[i].Line, secret), secret: sha256.Sum256([]byte(secret)),
		}
		if previous, duplicate := bySpan[key]; duplicate {
			if profile.PreferFinding(raw[i].RuleID, out[previous].RuleID) {
				clearGitleaksFinding(&out[previous])
				out[previous] = raw[i]
				clearGitleaksFinding(&raw[i])
			} else {
				clearGitleaksFinding(&raw[i])
			}
			continue
		}
		bySpan[key] = len(out)
		out = append(out, raw[i])
		clearGitleaksFinding(&raw[i])
	}
	return out
}

func clearGitleaksFinding(finding *report.Finding) {
	if finding == nil {
		return
	}
	finding.Secret, finding.Match, finding.Line = "", "", ""
	finding.Fragment = nil
}
