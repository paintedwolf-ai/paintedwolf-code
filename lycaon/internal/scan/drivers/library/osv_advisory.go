package library

import (
	"slices"
	"strings"

	"github.com/google/osv-scalibr/inventory"
	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/advisory/severity"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	osv "github.com/ossf/osv-schema/bindings/go/osvschema"
)

// Severity provenance stamped on advisory findings.
const (
	severitySourceCVSS             = "osv.cvss"
	severitySourceDatabase         = "osv.database_specific.severity"
	severitySourceMaliciousPackage = "osv.malicious_package"
)

// maliciousPackageOriginsField is the database_specific field that only
// malware reports carry; its presence is what classifies the record.
const maliciousPackageOriginsField = "malicious-packages-origins"

func packageAdvisory(pv *inventory.PackageVuln) (*api.AdvisoryRef, api.FindingLevel) {
	v := pv.Vulnerability
	ref := advisory.BuildAdvisoryRef(v.Id, v.Aliases...)
	ref.Kind = api.AdvisoryKindVulnerability
	severities := append([]*osv.Severity(nil), v.Severity...)
	if p := pv.Package; p != nil {
		ref.Package = &api.AdvisoryPackageRef{Name: strings.TrimSpace(p.Name), Version: strings.TrimSpace(p.Version), Ecosystem: scanfindings.NormalizePackageEcosystem(p.Ecosystem().String())}
		for _, affected := range v.Affected {
			pkg := affected.GetPackage()
			if pkg.GetName() != ref.Package.Name || scanfindings.NormalizePackageEcosystem(pkg.GetEcosystem()) != ref.Package.Ecosystem {
				continue
			}
			severities = append(severities, affected.Severity...)
			for _, r := range affected.Ranges {
				if r.GetType() == osv.Range_GIT {
					continue
				}
				for _, event := range r.Events {
					if fixed := event.GetFixed(); fixed != "" {
						ref.FixedVersions = append(ref.FixedVersions, fixed)
					}
				}
			}
		}
	}
	for _, r := range v.References {
		if u := r.GetUrl(); u != "" {
			ref.URLs = append(ref.URLs, u)
		}
	}
	ref.FixedVersions = sortedUnique(ref.FixedVersions)
	ref.URLs = sortedUnique(ref.URLs)
	level := advisorySeverity(ref, severities)
	if isMaliciousPackageRecord(v) {
		ref.Kind = api.AdvisoryKindMaliciousPackage
		ref.SeveritySource = severitySourceMaliciousPackage
		return ref, api.FindingLevelCritical
	}
	if level == api.FindingLevelUnknown {
		level = databaseSeverity(v)
		if level != api.FindingLevelUnknown {
			ref.SeveritySource = severitySourceDatabase
		}
	}
	if resolver, err := severity.Default(); err == nil && level == api.FindingLevelUnknown {
		if cvss, source, resolvedLevel, ok := resolver.Resolve(ref); ok {
			ref.CVSS = append(ref.CVSS, *cvss)
			ref.SeveritySource = source
			level = resolvedLevel
		}
	}
	return ref, level
}

func isMaliciousPackageRecord(v *osv.Vulnerability) bool {
	origins := v.GetDatabaseSpecific().GetFields()[maliciousPackageOriginsField].GetListValue()
	return len(origins.GetValues()) > 0
}

// databaseSeverity reads the severity band a record declares in
// database_specific when it publishes no CVSS vector.
func databaseSeverity(v *osv.Vulnerability) api.FindingLevel {
	switch strings.ToUpper(v.GetDatabaseSpecific().GetFields()["severity"].GetStringValue()) {
	case "CRITICAL":
		return api.FindingLevelCritical
	case "HIGH":
		return api.FindingLevelHigh
	case "MODERATE", "MEDIUM":
		return api.FindingLevelMedium
	case "LOW":
		return api.FindingLevelLow
	default:
		return api.FindingLevelUnknown
	}
}

func advisorySeverity(ref *api.AdvisoryRef, severities []*osv.Severity) api.FindingLevel {
	best := -1.0
	seen := map[string]bool{}
	for _, s := range severities {
		if s == nil || s.Score == "" {
			continue
		}
		key := s.Type.String() + ":" + s.Score
		if seen[key] {
			continue
		}
		seen[key] = true
		entry := api.AdvisoryCVSS{Type: s.Type.String(), Vector: s.Score}
		if score, ok := cvssScore(s); ok {
			entry.Score = &score
			best = max(best, score)
		}
		ref.CVSS = append(ref.CVSS, entry)
	}
	if best < 0 {
		return api.FindingLevelUnknown
	}
	ref.SeveritySource = severitySourceCVSS
	return severity.ScoreLevel(best)
}

// cvssScore scores a vector only when it matches the type the record declares.
func cvssScore(s *osv.Severity) (float64, bool) {
	score, cvssType, ok := severity.ParseVector(s.Score)
	return score, ok && cvssType == s.Type.String()
}

func sortedUnique(values []string) []string { slices.Sort(values); return slices.Compact(values) }
