package opengrep

import (
	"fmt"
	"strconv"
)

type Mode string

const (
	Intraprocedural Mode = "intraprocedural"
	Intrafile       Mode = "intrafile"
)

// Analysis is the catalog-owned semantic policy, shared by scans and conformance.
type Analysis struct {
	Mode Mode `yaml:"mode" json:"mode"`
}

func (a Analysis) Validate() error {
	switch a.Mode {
	case Intraprocedural, Intrafile:
		return nil
	default:
		return fmt.Errorf("unsupported Opengrep analysis mode %q", a.Mode)
	}
}

type Invocation struct {
	Analysis Analysis
	Output   string
	Rules    []string
	Targets  []string
	Excludes []string
	Jobs     int
}

func (v Invocation) Args() ([]string, error) {
	if err := v.Analysis.Validate(); err != nil {
		return nil, err
	}
	if v.Output == "" || len(v.Rules) == 0 || len(v.Targets) == 0 || v.Jobs < 1 {
		return nil, fmt.Errorf("opengrep requires output, rules, targets, and positive parallelism")
	}
	// Scope and cancellation belong to the host; engine defaults must not cut coverage.
	args := []string{
		"scan", "--timeout", "0", "--max-target-bytes", "0",
		"--quiet", "--no-rewrite-rule-ids", "--dataflow-traces", "--disable-version-check",
		"--json", "--output", v.Output, "--jobs", strconv.Itoa(v.Jobs),
	}
	if v.Analysis.Mode == Intrafile {
		args = append(args, "--taint-intrafile")
	}
	for _, pattern := range v.Excludes {
		args = append(args, "--exclude", pattern)
	}
	for _, path := range v.Rules {
		args = append(args, "--config", path)
	}
	return append(append(args, "--"), v.Targets...), nil
}
