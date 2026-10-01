package main

import (
	"context"
	"fmt"
	"math"
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

type projectBudget struct {
	Base      time.Duration
	Scale     int
	Effective time.Duration
}

func resolveProjectBudget(base time.Duration, scale int) (projectBudget, error) {
	if base <= 0 || scale < 1 || scale > 4 || int64(base) > math.MaxInt64/int64(scale) {
		return projectBudget{}, fmt.Errorf("project timeout must be positive and fit the scaled duration")
	}
	return projectBudget{Base: base, Scale: scale, Effective: base * time.Duration(scale)}, nil
}

func (b projectBudget) context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, b.Effective)
}
