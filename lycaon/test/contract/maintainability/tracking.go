package maintainability

import (
	"cmp"
	"slices"

	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func trackingReport(policy sizebudget.Policy, inv *inventory) *sizebudget.TrackingReport {
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
				Category: category, ID: id, Measured: value, Warn: limit.Warn, Limit: limit.Limit,
				EffectiveCap: cap, ExceptionReason: policy.Exceptions[category][id].Reason, Sources: files,
			})
		}
	}
	slices.SortFunc(report.Artifacts, func(a, b sizebudget.TrackingArtifact) int {
		return cmp.Or(cmp.Compare(a.Category, b.Category), cmp.Compare(a.ID, b.ID))
	})
	return report
}
