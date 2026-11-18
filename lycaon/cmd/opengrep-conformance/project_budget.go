package main

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

const defaultProjectTimeout = 10 * time.Minute

type projectEvaluationOptions struct {
	Repeats int
	Mode    opengrep.Mode
	Admit   opengrep.Mode
	Timeout time.Duration
}

// projectBudget is one project's scan deadline, the same on every host.
type projectBudget time.Duration

func resolveProjectBudget(timeout time.Duration) (projectBudget, error) {
	if timeout <= 0 {
		return 0, fmt.Errorf("project timeout must be positive")
	}
	return projectBudget(timeout), nil
}

func (b projectBudget) context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, time.Duration(b))
}
