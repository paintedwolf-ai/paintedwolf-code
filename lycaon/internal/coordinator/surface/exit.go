package surface

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
)

// ExitClass describes how a surface delivers its result.
type ExitClass string

const (
	// ExitReport delivers a grounded closeout envelope.
	ExitReport ExitClass = "report"
	// ExitVerdict delivers a review-loop verdict.
	ExitVerdict ExitClass = "verdict"
	// ExitPlan delivers a confirmed plan artifact.
	ExitPlan ExitClass = "plan"
	// ExitNone has no user-facing result.
	ExitNone ExitClass = "none"
)

// ParseExitClass validates a declared exit value.
func ParseExitClass(s string) (ExitClass, error) {
	switch c := ExitClass(strings.TrimSpace(s)); c {
	case ExitReport, ExitVerdict, ExitPlan, ExitNone:
		return c, nil
	default:
		return "", fmt.Errorf("invalid surface exit %q (want report|verdict|plan|none)", s)
	}
}

// SurfaceExit returns a surface's declared exit class.
func SurfaceExit(surfaceID string) (ExitClass, error) {
	exits, err := AllSurfaceExits()
	if err != nil {
		return "", err
	}
	c, ok := exits[strings.TrimSpace(surfaceID)]
	if !ok {
		return "", fmt.Errorf("unknown coordinator surface %q", surfaceID)
	}
	return c, nil
}

// AllSurfaceExits returns every surface's declared exit class.
func AllSurfaceExits() (map[string]ExitClass, error) {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	ids := catalog.SurfaceIDs()
	out := make(map[string]ExitClass, len(ids))
	for _, id := range ids {
		row, rowErr := catalog.Surface(id)
		if rowErr != nil {
			return nil, rowErr
		}
		c, err := ParseExitClass(row.Exit)
		if err != nil {
			return nil, fmt.Errorf("coordinator surface %q: %w", id, err)
		}
		out[id] = c
	}
	return out, nil
}

// SurfaceDeliversReport reports whether a surface delivers a report.
func SurfaceDeliversReport(surfaceID string) bool {
	c, err := SurfaceExit(surfaceID)
	return err == nil && c == ExitReport
}
