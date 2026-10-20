package packageexec

import (
	"context"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"strings"
)

// Preflight resolves identity for recognized package actions.
func (s *Service) Preflight(ctx context.Context, args map[string]any, projectDir string) (*Execution, error) {
	if s == nil || s.catalog == nil {
		return nil, nil
	}
	execution, ok := s.classifyArgs(args)
	if !ok {
		return nil, nil
	}
	return s.resolveExecution(ctx, execution, projectDir)
}

// PreflightStages consumes already bound argv without parsing display strings.
func (s *Service) PreflightStages(ctx context.Context, stages []exec.Stage, projectDir string) (*Execution, error) {
	if s == nil || s.catalog == nil {
		return nil, nil
	}
	execution, ok := s.catalog.classify(stages)
	if !ok {
		return nil, nil
	}
	return s.resolveExecution(ctx, execution, projectDir)
}

func (s *Service) resolveExecution(ctx context.Context, execution *Execution, projectDir string) (*Execution, error) {
	for i := range execution.Packages {
		s.resolve(ctx, &execution.Packages[i])
	}
	execution.AllowedHosts = normalizedStrings(append(execution.AllowedHosts, registryHosts(execution.Packages)...))
	execution.SensitiveReads = normalizedStrings(append(
		s.sensitive.ProtectedPathRoots(sensitivepath.ModeRead),
		s.sensitiveProjectReads(projectDir)...,
	))
	return execution, nil
}

// classifyArgs accepts command plans parsed by the command boundary.
func (s *Service) classifyArgs(args map[string]any) (*Execution, bool) {
	plan, err := commandsurface.ParsePlan(args)
	if err != nil {
		return nil, false
	}
	return s.catalog.classify(plan.Stages)
}

// ClassifyTerminalInput identifies package execution in PTY input.
func (s *Service) ClassifyTerminalInput(input string) (*Execution, bool) {
	if s == nil || s.catalog == nil {
		return nil, false
	}
	normalized := strings.ReplaceAll(input, "{Enter}", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		plan, err := commandsurface.ParsePlan(map[string]any{"command": line})
		if err != nil {
			continue
		}
		if execution, ok := s.catalog.classify(plan.Stages); ok {
			return execution, true
		}
	}
	return nil, false
}
