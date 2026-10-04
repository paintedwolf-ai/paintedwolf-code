package report

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

// notCoveredItems separates unresolved work from disclosed tool limitations.
func notCoveredItems(input ReportInput) []string {
	var out []string
	reviewed := input.CoverageFacts != nil && input.CoverageReview != nil && reviewcoverage.Validate(*input.CoverageFacts, *input.CoverageReview) == nil
	for _, kind := range gapOrder {
		for _, g := range input.Gaps {
			if g.Kind != kind || g.Count == 0 {
				continue
			}
			if reviewed && (kind == GapScansMoved || kind == GapLegsPartial || kind == GapWorkersPartial) {
				continue
			}
			if kind == GapClaimsOpen && input.openClaimsAssessed() {
				continue
			}
			if item := gapItem(g); item != "" {
				out = append(out, item)
			}
		}
	}
	if reviewed {
		for _, a := range input.CoverageReview.Assessments {
			if a.Disposition == reviewcoverage.MaterialOpen || a.Disposition == reviewcoverage.EssentialOpen {
				out = append(out, coverageAssessmentText(input, a))
			}
		}
	}
	return out
}

func scannerLimitItems(input ReportInput) []string {
	var out []string
	for _, g := range input.Gaps {
		if g.Kind == GapScansStanding && g.Count > 0 {
			out = append(out, gapItem(g))
		}
	}
	return out
}

func exclusionItems(input ReportInput) []string {
	var out []string
	if inv := input.Inventory; inv != nil {
		for _, sa := range inv.SetAsides {
			if sa.Groups > 0 {
				out = append(out, fmt.Sprintf("%s accounted for: %s", plural(sa.Groups, "result group", "result groups"), sa.Reason))
			}
		}
	}
	return out
}

func coverageAssessmentItems(input ReportInput) []string {
	if input.CoverageReview == nil {
		return nil
	}
	var out []string
	for _, a := range input.CoverageReview.Assessments {
		out = append(out, coverageAssessmentText(input, a))
	}
	return out
}

func coverageAssessmentText(input ReportInput, a api.CoverageAssessment) string {
	subject := a.ID
	if input.CoverageFacts != nil {
		for _, rows := range [][]reviewcoverage.Fact{input.CoverageFacts.Obligations, input.CoverageFacts.Gaps} {
			for _, f := range rows {
				if f.ID == a.ID {
					subject = f.Subject
					if f.Kind == "review_question" && f.Question != "" {
						subject = f.Question + " (" + f.Subject + ")"
					}
					if len(f.Paths) > 0 {
						subject += fmt.Sprintf(" · %s: %s", plural(f.FileCount, "affected file", "affected files"), strings.Join(f.Paths, ", "))
						if f.FileCount > len(f.Paths) {
							subject += fmt.Sprintf(" (and %d more)", f.FileCount-len(f.Paths))
						}
					}
				}
			}
		}
	}
	var cites []string
	for _, c := range a.CitedEvidence {
		if c.Handle != "" {
			cites = append(cites, c.Handle)
		} else {
			cites = append(cites, fmt.Sprintf("%s:%d", c.Path, c.Line))
		}
	}
	return subject + " — " + strings.ReplaceAll(a.Disposition, "_", " ") + ": " + a.Reason + " Evidence: " + strings.Join(cites, ", ") + "."
}

func gapItem(g ReportGap) string {
	names := strings.Join(g.Names, ", ")
	switch g.Kind {
	case GapInventoryUnaccounted:
		return fmt.Sprintf("%d of %d scanner result groups have no assessment and no set-aside. Unassessed is not cleared.", g.Count, g.Of)
	case GapClaimsOpen:
		return fmt.Sprintf("%s still open: %s.", plural(g.Count, "claim", "claims"), names)
	case GapLegsUnfinished:
		return "Planned areas not checked: " + names + "."
	case GapLegsPartial:
		return "Areas only partly checked: " + names + "."
	case GapWorkersPartial:
		return fmt.Sprintf("%s ended partial: %s.", plural(g.Count, "supporting task", "supporting tasks"), names)
	case GapScansFailed:
		return "Scans that failed: " + names + "."
	case GapScansMoved:
		if g.UnknownScope {
			return names + ": source changed during scanning; the full affected scope is unknown and has not been rescanned."
		}
		return fmt.Sprintf("%s: %s changed while the scan ran and %s not rescanned.",
			names, plural(g.Detail, "file", "files"), noun(g.Detail, "was", "were"))
	case GapScansStanding:
		return fmt.Sprintf("Recorded scanner limits: %s could not fully analyze %s in %s.",
			names, plural(g.Detail, "construct", "constructs"), plural(g.DetailFiles, "file", "files"))
	default:
		return ""
	}
}
