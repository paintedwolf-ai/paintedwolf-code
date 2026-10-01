package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/runnerbudget"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

type projectCorpus struct {
	Projects  []projectCase    `yaml:"projects"`
	Partition string           `yaml:"partition"`
	Admission *admissionPolicy `yaml:"admission"`
}

type projectCase struct {
	Name         string                                        `yaml:"name"`
	Language     string                                        `yaml:"language"`
	Framework    string                                        `yaml:"framework"`
	Families     []string                                      `yaml:"families"`
	Provenance   *projectProvenance                            `yaml:"provenance"`
	Adjudication string                                        `yaml:"adjudication"`
	ReviewStatus string                                        `yaml:"review_status"`
	Files        map[string]string                             `yaml:"files"`
	Findings     []projectFinding                              `yaml:"findings"`
	Diagnostics  map[opengrep.Mode][]expectedProjectDiagnostic `yaml:"diagnostics"`
}

type projectFinding struct {
	File   string `yaml:"file" json:"file"`
	Rule   string `yaml:"rule" json:"rule"`
	Line   int    `yaml:"line" json:"line"`
	Column int    `yaml:"column,omitempty" json:"column,omitempty"`
}

type projectMeasurement struct {
	Project             string                      `json:"project"`
	ReviewStatus        string                      `json:"review_status,omitempty"`
	Language            string                      `json:"language"`
	Framework           string                      `json:"framework,omitempty"`
	Families            map[string]familyCounts     `json:"families,omitempty"`
	Mode                opengrep.Mode               `json:"mode"`
	Run                 int                         `json:"run"`
	Milliseconds        int64                       `json:"milliseconds"`
	CPUTimeMilliseconds int64                       `json:"reported_cpu_milliseconds"`
	TruePositive        int                         `json:"true_positive"`
	FalsePositive       int                         `json:"false_positive"`
	FalseNegative       int                         `json:"false_negative"`
	Unresolved          int                         `json:"unresolved"`
	Findings            []projectFinding            `json:"findings"`
	OutputBytes         int64                       `json:"output_bytes"`
	MemorySamples       int                         `json:"memory_samples"`
	MemoryFailure       string                      `json:"memory_measurement_failure,omitempty"`
	PeakRSSBytes        int64                       `json:"sampled_process_tree_peak_rss_bytes,omitempty"`
	Duplicates          int                         `json:"duplicates"`
	SourceBytes         int                         `json:"source_bytes"`
	SourceFiles         int                         `json:"source_files"`
	Failure             string                      `json:"failure,omitempty"`
	Diagnostics         []expectedProjectDiagnostic `json:"diagnostics,omitempty"`
}

func loadProjects(path string) (*projectCorpus, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- explicit developer corpus path.
	if err != nil {
		return nil, err
	}
	var corpus projectCorpus
	if err := decodeCorpus(raw, &corpus); err != nil {
		return nil, err
	}
	if len(corpus.Projects) == 0 {
		return nil, fmt.Errorf("project corpus is empty")
	}
	names := map[string]bool{}
	units := map[string]bool{}
	for _, project := range corpus.Projects {
		if project.ReviewStatus != "" && project.ReviewStatus != "complete" && project.ReviewStatus != "pending" {
			return nil, fmt.Errorf("%s: unknown review status %q", project.Name, project.ReviewStatus)
		}
		if project.Name == "" || names[project.Name] || project.Language == "" || strings.TrimSpace(project.Adjudication) == "" || len(project.Files) == 0 {
			return nil, fmt.Errorf("invalid or duplicate project %q", project.Name)
		}
		names[project.Name] = true
		if corpus.Partition == "held_out" {
			if err := validateHeldOutProject(project, units); err != nil {
				return nil, err
			}
		} else if project.Provenance != nil {
			if err := validateProjectProvenance(project); err != nil {
				return nil, err
			}
		}
		for path, source := range project.Files {
			if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path || source == "" {
				return nil, fmt.Errorf("%s: invalid source %q", project.Name, path)
			}
		}
		if err := validateProjectDiagnostics(project); err != nil {
			return nil, err
		}
		seen := map[projectFinding]bool{}
		for _, finding := range project.Findings {
			if project.Files[finding.File] == "" || finding.Rule == "" || finding.Line < 1 || finding.Line > strings.Count(project.Files[finding.File], "\n")+1 || finding.Column < 0 || seen[finding] {
				return nil, fmt.Errorf("%s: invalid expected finding %+v", project.Name, finding)
			}
			seen[finding] = true
		}
	}
	if corpus.Partition != "" && corpus.Partition != "development" && corpus.Partition != "held_out" {
		return nil, fmt.Errorf("unknown corpus partition %q", corpus.Partition)
	}
	if corpus.Admission != nil {
		if err := corpus.Admission.validate(); err != nil {
			return nil, err
		}
	}
	return &corpus, nil
}

func evaluateProjects(binary, path, candidateRules string, options projectEvaluationOptions) error {
	if options.Repeats < 1 || options.Repeats > 10 {
		return fmt.Errorf("repeats must be between 1 and 10")
	}
	modes, err := projectEvaluationModes(options.Mode, options.Admit)
	if err != nil {
		return err
	}
	budget, err := resolveProjectBudget(options.Timeout, runnerbudget.TimeoutScale())
	if err != nil {
		return err
	}
	admit := options.Admit
	corpus, err := loadProjects(path)
	if err != nil {
		return err
	}
	var policy *admissionPolicy
	if admit != "" {
		if err := (opengrep.Analysis{Mode: admit}).Validate(); err != nil {
			return err
		}
		if corpus.Partition != "held_out" || corpus.Admission == nil {
			return fmt.Errorf("admission requires a held-out corpus with thresholds fixed before evaluation")
		}
		for _, project := range corpus.Projects {
			if project.ReviewStatus != "complete" {
				return fmt.Errorf("%s: admission requires completed source adjudication", project.Name)
			}
		}
		policy = corpus.Admission
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	bundle, err := loadProjectRules(candidateRules, root)
	if err != nil {
		return err
	}
	if err := validateProjectFamilies(corpus.Projects, bundle); err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := writeEvaluationIdentity(encoder, binary, bundle, path, admit != "", budget); err != nil {
		return err
	}
	var failures []error
	var measurements []projectMeasurement
	for _, project := range corpus.Projects {
		for _, mode := range modes {
			var previous []projectFinding
			for run := 1; run <= options.Repeats; run++ {
				measurement := measureProject(binary, bundle, project, mode, run, budget)
				if run > 1 && !slices.Equal(previous, measurement.Findings) {
					measurement.Failure += " unstable finding set across repetitions"
				}
				previous = measurement.Findings
				if measurement.Failure != "" {
					failures = append(failures, fmt.Errorf("%s/%s: %s", project.Name, mode, measurement.Failure))
				}
				measurements = append(measurements, measurement)
				if err := encoder.Encode(measurement); err != nil {
					return err
				}
			}
		}
	}
	failures = append(failures, writeFamilySummaries(encoder, measurements, admit, policy, corpus.Partition))
	failures = append(failures, writeSummaries(encoder, measurements, admit, policy, corpus.Partition))
	return errors.Join(failures...)
}

func writeEvaluationIdentity(encoder *json.Encoder, binary string, bundle []byte, corpus string, admitting bool, budget projectBudget) error {
	path, err := exec.LookPath(binary)
	if err != nil {
		return err
	}
	engine, err := os.Open(path) // #nosec G304 -- explicit developer scanner executable.
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	format := executableFormat(engine)
	if admitting && format == "launcher" {
		return fmt.Errorf("admission requires the standalone engine artifact; a launcher digest does not bind its interpreter or core")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, engine); err != nil {
		return err
	}
	fixtures, err := os.ReadFile(corpus) // #nosec G304 -- explicit developer corpus path.
	if err != nil {
		return err
	}
	return encoder.Encode(map[string]string{
		"type": "evaluation_identity", "engine_sha256": fmt.Sprintf("%x", hash.Sum(nil)), "engine_artifact_format": format,
		"rules_sha256": fmt.Sprintf("%x", sha256.Sum256(bundle)), "corpus_sha256": fmt.Sprintf("%x", sha256.Sum256(fixtures)),
		"test_timeout_scale": fmt.Sprint(budget.Scale),
		"test_base_deadline": budget.Base.String(),
		"test_deadline":      budget.Effective.String(),
		"scanner_jobs":       "1", "scanner_rule_timeout": "0",
		"cache_policy":         "Fresh scanner process and working directory per run; OS and executable extraction caches are uncontrolled",
		"resource_measurement": "Launcher process-state CPU; aggregate worker RSS sampled every 20 ms; shared pages may be counted per process",
	})
}

func measureProject(binary string, bundle []byte, project projectCase, mode opengrep.Mode, run int, budget projectBudget) projectMeasurement {
	m := projectMeasurement{Project: project.Name, ReviewStatus: project.ReviewStatus, Language: project.Language, Framework: project.Framework, Mode: mode, Run: run, Families: make(map[string]familyCounts)}
	for _, family := range project.Families {
		m.Families[family] = familyCounts{}
	}
	m.SourceFiles = len(project.Files)
	for _, source := range project.Files {
		m.SourceBytes += len(source)
	}
	start := time.Now()
	report, err := scanProject(binary, bundle, project, mode, budget)
	m.Milliseconds = time.Since(start).Milliseconds()
	if report != nil {
		m.OutputBytes = report.OutputBytes
		m.PeakRSSBytes = report.PeakRSSBytes
		m.MemorySamples = report.MemorySamples
		m.MemoryFailure = report.MemoryFailure
		m.CPUTimeMilliseconds = report.CPUTimeMilliseconds
		m.Diagnostics = projectDiagnostics(report.Warnings)
	}
	if err != nil {
		m.Failure = err.Error()
		return m
	}
	if err := compareProjectDiagnostics(project, mode, m.Diagnostics); err != nil {
		m.Failure = err.Error()
	}
	classifyProjectFindings(&m, project, report)
	return m
}

func scanProject(binary string, bundle []byte, project projectCase, mode opengrep.Mode, budget projectBudget) (*scanReport, error) {
	dir, err := os.MkdirTemp("", "opengrep-project-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for path, source := range project.Files {
		_, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: fseffect.Location{Root: dir, Rel: filepath.Join("source", path)},
			Source:   strings.NewReader(source), Mode: 0o600, DirMode: 0o750,
		})
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := budget.context(context.Background())
	defer cancel()
	return scan(ctx, binary, dir, bundle, opengrep.Analysis{Mode: mode})
}
