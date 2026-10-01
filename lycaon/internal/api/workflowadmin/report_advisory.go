package workflowadmin

import (
	"fmt"
	"strings"

	advisoryids "github.com/lycaon/lycaon/internal/advisory"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func reportAdvisoryDetails(advisory *wire.AdvisoryRef) []string {
	if advisory == nil {
		return nil
	}
	var out []string
	if advisory.Kind == wire.AdvisoryKindMaliciousPackage {
		out = append(out, "Malicious package: the advisory database records this package version as malware, not as a vulnerable dependency.")
	}
	if ids := advisoryids.IDs(advisory); len(ids) > 1 {
		out = append(out, "Advisory ids: "+strings.Join(ids, ", "))
	}
	if advisory.SeveritySource != "" {
		out = append(out, "Severity source: "+advisory.SeveritySource)
	}
	for _, vector := range advisory.CVSS {
		score := "unparsed"
		if vector.Score != nil {
			score = fmt.Sprintf("%.1f", *vector.Score)
		}
		out = append(out, vector.Type+" ("+score+"): "+vector.Vector)
	}
	if len(advisory.FixedVersions) > 0 {
		out = append(out, "Published fixed boundaries: "+strings.Join(advisory.FixedVersions, ", ")+". Applicability and upgrade compatibility require assessment.")
	}
	for _, url := range advisory.URLs[:min(3, len(advisory.URLs))] {
		out = append(out, "Advisory reference: "+url)
	}
	if len(advisory.URLs) > 3 {
		out = append(out, fmt.Sprintf("%d additional advisory references in the scan ledger.", len(advisory.URLs)-3))
	}
	return out
}
