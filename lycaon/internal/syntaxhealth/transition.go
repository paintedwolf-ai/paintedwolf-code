package syntaxhealth

import (
	"context"

	"github.com/lycaon/lycaon/internal/tsparse"
)

// Transition classifies a proposed source replacement.
type Transition string

const (
	TransitionAllowed         Transition = "allowed"
	TransitionNewFileBroken   Transition = "new_file_broken"
	TransitionBrokeClean      Transition = "broke_clean"
	TransitionRepairRegressed Transition = "repair_regressed"
	TransitionFinalBroken     Transition = "final_broken"
	TransitionParseIncomplete Transition = "parse_incomplete"
	TransitionParseFailed     Transition = "parse_failed"
)

// Change is a parser-health decision for one before/after pair. Before is nil
// when a new or clean candidate needs no original-file comparison.
type Change struct {
	Transition Transition
	Before     *Report
	After      Report
}

// EvaluateFinal requires clean syntax at completion and promotion.
func EvaluateFinal(ctx context.Context, filename string, after []byte) Change {
	if overridden(ctx) {
		return Change{Transition: TransitionAllowed, After: Report{Status: StatusOverridden}}
	}
	report := Analyze(ctx, "", filename, after, tsparse.Validation)
	change := Change{After: report, Transition: TransitionAllowed}
	switch report.Status {
	case StatusBroken:
		change.Transition = TransitionFinalBroken
	case StatusIncomplete:
		change.Transition = TransitionParseIncomplete
	case StatusFailed:
		change.Transition = TransitionParseFailed
	case StatusUnsupported, StatusOverridden, StatusClean:
	}
	return change
}

// Evaluate accepts clean source or a strict reduction in existing syntax faults.
func Evaluate(ctx context.Context, filename string, before []byte, beforeExists bool, after []byte) Change {
	if overridden(ctx) {
		return Change{Transition: TransitionAllowed, After: Report{Status: StatusOverridden}}
	}
	afterReport := Analyze(ctx, "", filename, after, tsparse.Validation)
	if afterReport.Status == StatusIncomplete {
		return Change{Transition: TransitionParseIncomplete, After: afterReport}
	}
	if afterReport.Status == StatusFailed {
		return Change{Transition: TransitionParseFailed, After: afterReport}
	}
	if afterReport.Status == StatusClean {
		return Change{Transition: TransitionAllowed, After: afterReport}
	}
	if !beforeExists {
		if afterReport.Status == StatusBroken {
			return Change{Transition: TransitionNewFileBroken, After: afterReport}
		}
		return Change{Transition: TransitionAllowed, After: afterReport}
	}
	beforeReport := Analyze(ctx, "", filename, before, tsparse.Validation)
	if afterReport.Status == StatusUnsupported && beforeReport.Status != StatusUnsupported {
		afterReport = Analyze(ctx, beforeReport.Language, filename, after, tsparse.Validation)
	}
	change := Change{Before: &beforeReport, After: afterReport}
	if afterReport.Status == StatusUnsupported {
		change.Transition = TransitionAllowed
		return change
	}
	if afterReport.Status == StatusIncomplete {
		change.Transition = TransitionParseIncomplete
		return change
	}
	if afterReport.Status == StatusFailed {
		change.Transition = TransitionParseFailed
		return change
	}
	if afterReport.Status == StatusClean {
		change.Transition = TransitionAllowed
		return change
	}
	if beforeReport.Status == StatusIncomplete {
		change.Transition = TransitionParseIncomplete
		return change
	}
	if beforeReport.Status == StatusFailed {
		change.Transition = TransitionParseFailed
		return change
	}
	if beforeReport.Status == StatusUnsupported {
		change.Transition = TransitionNewFileBroken
		return change
	}
	if beforeReport.Status == StatusClean {
		if afterReport.Status == StatusBroken {
			change.Transition = TransitionBrokeClean
			return change
		}
		change.Transition = TransitionAllowed
		return change
	}
	if burdenImproved(beforeReport.Burden, afterReport.Burden) {
		change.Transition = TransitionAllowed
		return change
	}
	change.Transition = TransitionRepairRegressed
	return change
}

func burdenImproved(before, after Burden) bool {
	if after.SpanBytes != before.SpanBytes {
		return after.SpanBytes < before.SpanBytes
	}
	return after.Faults < before.Faults
}
