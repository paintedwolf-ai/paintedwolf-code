package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

type admissionPolicy struct {
	MinimumTruePositives              int     `yaml:"minimum_true_positives"`
	MinimumProjectPrecisionLowerBound float64 `yaml:"minimum_project_precision_lower_bound"`
	MaximumFalsePositives             int     `yaml:"maximum_false_positives"`
	MaximumFalseNegatives             int     `yaml:"maximum_false_negatives"`
	MaximumP95Milliseconds            int64   `yaml:"maximum_p95_milliseconds"`
	MaximumPeakRSSBytes               int64   `yaml:"maximum_peak_rss_bytes"`
	MaximumOutputBytes                int64   `yaml:"maximum_output_bytes"`
}

type evaluationSummary struct {
	Type                   string        `json:"type"`
	Mode                   opengrep.Mode `json:"mode"`
	Projects               int           `json:"projects"`
	PendingProjects        int           `json:"pending_projects"`
	TruePositive           int           `json:"true_positive"`
	FalsePositive          int           `json:"false_positive"`
	FalseNegative          int           `json:"false_negative"`
	Unresolved             int           `json:"unresolved"`
	Failures               int           `json:"failures"`
	CoverageDiagnostics    int           `json:"coverage_diagnostics"`
	FindingProjects        int           `json:"finding_projects"`
	PerfectFindingProjects int           `json:"perfect_finding_projects"`
	Precision              *float64      `json:"precision"`
	PrecisionLowerBound    *float64      `json:"project_precision_95_percent_lower_bound"`
	P95Milliseconds        int64         `json:"p95_milliseconds"`
	UnmeasuredRuns         int           `json:"unmeasured_runs"`
	PeakRSSBytes           int64         `json:"sampled_process_tree_peak_rss_bytes"`
	OutputBytes            int64         `json:"max_output_bytes"`
	Rejection              []string      `json:"admission_rejection,omitempty"`
}

// Repeated scans measure stability and resources; they are not independent samples.
func summarizeProjects(mode opengrep.Mode, measurements []projectMeasurement) evaluationSummary {
	summary := evaluationSummary{Type: "evaluation_summary", Mode: mode}
	var times []int64
	for _, m := range measurements {
		if m.Mode != mode {
			continue
		}
		times = append(times, m.Milliseconds)
		summary.PeakRSSBytes = max(summary.PeakRSSBytes, m.PeakRSSBytes)
		if m.MemorySamples == 0 || m.MemoryFailure != "" || m.PeakRSSBytes <= 0 {
			summary.UnmeasuredRuns++
		}
		summary.OutputBytes = max(summary.OutputBytes, m.OutputBytes)
		if m.Failure != "" {
			summary.Failures++
		}
		if m.Run != 1 {
			continue
		}
		summary.Projects++
		if m.ReviewStatus == "pending" {
			summary.PendingProjects++
		}
		summary.CoverageDiagnostics += len(m.Diagnostics)
		if m.TruePositive+m.FalsePositive+m.Unresolved > 0 {
			summary.FindingProjects++
			if m.FalsePositive == 0 && m.Unresolved == 0 {
				summary.PerfectFindingProjects++
			}
		}
		summary.TruePositive += m.TruePositive
		summary.FalsePositive += m.FalsePositive
		summary.FalseNegative += m.FalseNegative
		summary.Unresolved += m.Unresolved
	}
	slices.Sort(times)
	if len(times) > 0 {
		summary.P95Milliseconds = times[int(math.Ceil(float64(len(times))*0.95))-1]
	}
	total := summary.TruePositive + summary.FalsePositive
	if total > 0 && summary.Unresolved == 0 && summary.PendingProjects == 0 {
		precision := float64(summary.TruePositive) / float64(total)
		lower := wilsonLower(summary.PerfectFindingProjects, summary.FindingProjects)
		summary.Precision = &precision
		summary.PrecisionLowerBound = &lower
	}
	return summary
}

func wilsonLower(success, total int) float64 {
	if total == 0 {
		return 0
	}
	n, z := float64(total), 1.959963984540054
	p := float64(success) / n
	return (p + z*z/(2*n) - z*math.Sqrt(p*(1-p)/n+z*z/(4*n*n))) / (1 + z*z/n)
}

func (p admissionPolicy) validate() error {
	if p.MinimumTruePositives < 1 || p.MinimumProjectPrecisionLowerBound <= 0 || p.MinimumProjectPrecisionLowerBound >= 1 ||
		p.MaximumFalsePositives < 0 || p.MaximumFalseNegatives < 0 || p.MaximumP95Milliseconds < 1 || p.MaximumPeakRSSBytes < 1 || p.MaximumOutputBytes < 1 {
		return fmt.Errorf("admission policy requires explicit positive evidence and resource bounds")
	}
	return nil
}

func (p admissionPolicy) reject(s evaluationSummary) []string {
	var reasons []string
	if s.Failures > 0 {
		reasons = append(reasons, "scan or repeatability failures")
	}
	if s.CoverageDiagnostics > 0 {
		reasons = append(reasons, "coverage diagnostics remain in the admitted scope")
	}
	if s.TruePositive < p.MinimumTruePositives {
		reasons = append(reasons, "insufficient adjudicated true positives")
	}
	if s.FalsePositive > p.MaximumFalsePositives {
		reasons = append(reasons, "false-positive budget exceeded")
	}
	if s.FalseNegative > p.MaximumFalseNegatives {
		reasons = append(reasons, "supported detection contract has misses")
	}
	if s.Unresolved > 0 {
		reasons = append(reasons, "unresolved findings require source adjudication")
	}
	if s.PendingProjects > 0 {
		reasons = append(reasons, "project source review remains incomplete")
	}
	if s.PrecisionLowerBound == nil || *s.PrecisionLowerBound < p.MinimumProjectPrecisionLowerBound {
		reasons = append(reasons, "project precision uncertainty exceeds admission bound")
	}
	if s.P95Milliseconds > p.MaximumP95Milliseconds {
		reasons = append(reasons, "latency budget exceeded")
	}
	if s.PeakRSSBytes == 0 || s.UnmeasuredRuns > 0 {
		reasons = append(reasons, "peak memory measurement unavailable")
	}
	if s.PeakRSSBytes > p.MaximumPeakRSSBytes {
		reasons = append(reasons, "memory budget exceeded")
	}
	if s.OutputBytes > p.MaximumOutputBytes {
		reasons = append(reasons, "output budget exceeded")
	}
	return reasons
}

func writeSummaries(encoder *json.Encoder, measurements []projectMeasurement, admit opengrep.Mode, policy *admissionPolicy, partition string) error {
	var rejection []string
	for _, mode := range []opengrep.Mode{opengrep.Intraprocedural, opengrep.Intrafile} {
		summary := summarizeProjects(mode, measurements)
		if summary.Projects == 0 {
			continue
		}
		if mode == admit && policy != nil {
			summary.Rejection = policy.reject(summary)
			rejection = summary.Rejection
		}
		if partition != "held_out" {
			summary.PrecisionLowerBound = nil
		}
		if err := encoder.Encode(summary); err != nil {
			return err
		}
	}
	if len(rejection) > 0 {
		return fmt.Errorf("admission rejected: %s", strings.Join(rejection, "; "))
	}
	return nil
}
