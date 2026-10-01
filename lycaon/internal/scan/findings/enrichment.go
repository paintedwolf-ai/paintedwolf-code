package findings

import (
	"regexp"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/advisory/severity"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	embeddedCVEPattern  = regexp.MustCompile(`(?i)\b(CVE-\d{4}-\d{4,})\b`)
	embeddedGHSAPattern = regexp.MustCompile(`(?i)\b(GHSA(-[a-z0-9]{4}){3})\b`)
)

// RuleAdvisory reads the advisory a scanner names in its rule id. It depends
// only on that id, so a finding's identity never varies with the severity catalog.
func RuleAdvisory(ruleID string) *api.AdvisoryRef {
	if match := embeddedCVEPattern.FindString(ruleID); match != "" {
		return advisory.BuildAdvisoryRef(advisory.NormalizeCVE(match))
	}
	if match := embeddedGHSAPattern.FindString(ruleID); match != "" {
		return advisory.BuildAdvisoryRef(advisory.NormalizeGHSA(match))
	}
	return nil
}

// EnrichSecurityFinding rates an unrated finding from the severity catalog. It
// adds CVSS evidence to the finding's advisory and never changes its identity.
func EnrichSecurityFinding(f *api.SecurityFinding, resolver severity.Resolver) {
	if f == nil || resolver == nil {
		return
	}
	if f.Level != "" && f.Level != api.FindingLevelUnknown {
		return
	}
	adv := Advisory(*f)
	if adv == nil {
		return
	}
	if cvss, source, level, ok := resolver.Resolve(adv); ok {
		adv.CVSS = append(adv.CVSS, *cvss)
		adv.SeveritySource = source
		f.Level = level
	}
}
