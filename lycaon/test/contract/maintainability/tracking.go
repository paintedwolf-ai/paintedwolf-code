package maintainability

import (
	"cmp"
	"slices"

	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func trackingReport(policy sizebudget.Policy, inv *inventory, touched sizebudget.Touched) *sizebudget.TrackingReport {
	report := &sizebudget.TrackingReport{SchemaVersion: 1, Complete: true, Artifacts: []sizebudget.TrackingArtifact{}}
	sources := artifactSources(inv)
	for category, artifacts := range inv.measured {
		limit := policy.Limits[category]
		for id, value := range artifacts {
			if value <= limit.Warn {
				continue
			}
			cap, _ := policy.Cap(category, id)
			files := slices.Clone(sources[category][id])
			slices.Sort(files)
			report.Artifacts = append(report.Artifacts, sizebudget.TrackingArtifact{
				Category: category, ID: id, Touched: touched(category, id), Measured: value, Warn: limit.Warn, Limit: limit.Limit,
				Spans: trackingSpans(inv, category, id), EffectiveCap: cap, ExceptionReason: policy.Exceptions[category][id].Reason, Sources: files,
			})
		}
	}
	slices.SortFunc(report.Artifacts, func(a, b sizebudget.TrackingArtifact) int {
		return cmp.Or(cmp.Compare(a.Category, b.Category), cmp.Compare(a.ID, b.ID))
	})
	return report
}

func trackingSpans(inv *inventory, category, id string) []sizebudget.TrackingSpan {
	spans := inv.methodSpans[id]
	if category == "go_struct_fields" {
		spans = inv.declarations[id]
	} else if category != "go_receiver_lines" && category != "go_receiver_methods" {
		spans = nil
	}
	out := make([]sizebudget.TrackingSpan, 0, len(spans))
	for _, s := range spans {
		out = append(out, sizebudget.TrackingSpan{File: s.file, First: s.first, Last: s.last})
	}
	slices.SortFunc(out, func(a, b sizebudget.TrackingSpan) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.First, b.First), cmp.Compare(a.Last, b.Last))
	})
	return out
}
