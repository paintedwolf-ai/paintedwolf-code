package severity

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
	cvss20 "github.com/pandatix/go-cvss/20"
	cvss30 "github.com/pandatix/go-cvss/30"
	cvss31 "github.com/pandatix/go-cvss/31"
	cvss40 "github.com/pandatix/go-cvss/40"
)

const (
	SourceNVDCVSS  = "nvd.cvss"
	SourceGHSACVSS = "ghsa.cvss"
)

// Entry holds an authoritative CVSS rating for an advisory alias.
type Entry struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Vector string  `json:"vector"`
	Score  float64 `json:"score"`
	Source string  `json:"source"`
}

// ParseVector validates a CVSS vector and computes its base score.
func ParseVector(vector string) (float64, string, bool) {
	vector = strings.TrimSpace(vector)
	switch {
	case strings.HasPrefix(vector, "CVSS:4.0/"):
		if v, err := cvss40.ParseVector(vector); err == nil {
			return v.Score(), "CVSS_V4", true
		}
	case strings.HasPrefix(vector, "CVSS:3.1/"):
		if v, err := cvss31.ParseVector(vector); err == nil {
			return v.BaseScore(), "CVSS_V3", true
		}
	case strings.HasPrefix(vector, "CVSS:3.0/"):
		if v, err := cvss30.ParseVector(vector); err == nil {
			return v.BaseScore(), "CVSS_V3", true
		}
	case strings.HasPrefix(vector, "AV:"):
		if v, err := cvss20.ParseVector(vector); err == nil {
			return v.BaseScore(), "CVSS_V2", true
		}
	}
	return 0, "", false
}

// ScoreLevel maps a CVSS base score (0.0–10.0) to a FindingLevel.
func ScoreLevel(score float64) api.FindingLevel {
	switch {
	case score >= 9.0:
		return api.FindingLevelCritical
	case score >= 7.0:
		return api.FindingLevelHigh
	case score >= 4.0:
		return api.FindingLevelMedium
	case score > 0.0:
		return api.FindingLevelLow
	default:
		return api.FindingLevelInfo
	}
}
