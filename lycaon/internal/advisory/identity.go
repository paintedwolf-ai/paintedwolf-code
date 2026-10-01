// Package advisory keys a dependency finding by one vulnerability rather than
// by the database record that matched. A vulnerability is published under a
// CVE, a GHSA, and one id per ecosystem database, each record listing the
// others as aliases; the canonical id is chosen from the whole alias set.
package advisory

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

var (
	cvePattern  = regexp.MustCompile(`(?i)^CVE-\d{4}-\d{4,}$`)
	ghsaPattern = regexp.MustCompile(`(?i)^GHSA(-[a-z0-9]{4}){3}$`)
)

// NormalizeCVE returns the canonical CVE spelling, or "" when raw is not a CVE id.
func NormalizeCVE(raw string) string {
	s := strings.TrimSpace(raw)
	if !cvePattern.MatchString(s) {
		return ""
	}
	return strings.ToUpper(s)
}

// NormalizeGHSA returns a GHSA id as uppercase prefix and lowercase suffix, or
// "" when raw is not a GHSA id.
func NormalizeGHSA(raw string) string {
	s := strings.TrimSpace(raw)
	if !ghsaPattern.MatchString(s) {
		return ""
	}
	return "GHSA" + strings.ToLower(s[len("GHSA"):])
}

// Normalize rebuilds ref's identity from every id it carries: each id lands in
// the list for its family, sorted and unique, and OSVID becomes the first CVE,
// else the first GHSA, else the first database id. Idempotent.
func Normalize(ref *api.AdvisoryRef) {
	if ref == nil {
		return
	}
	var cves, ghsas, databases []string
	for _, raw := range collect(ref) {
		switch {
		case NormalizeCVE(raw) != "":
			cves = append(cves, NormalizeCVE(raw))
		case NormalizeGHSA(raw) != "":
			ghsas = append(ghsas, NormalizeGHSA(raw))
		case strings.TrimSpace(raw) != "":
			databases = append(databases, strings.TrimSpace(raw))
		}
	}
	ref.CVEIDs = sortedUnique(cves)
	ref.GHSAIDs = sortedUnique(ghsas)
	ref.Aliases = sortedUnique(databases)
	switch {
	case len(ref.CVEIDs) > 0:
		ref.OSVID = ref.CVEIDs[0]
	case len(ref.GHSAIDs) > 0:
		ref.OSVID = ref.GHSAIDs[0]
	case len(ref.Aliases) > 0:
		ref.OSVID = ref.Aliases[0]
	default:
		ref.OSVID = ""
	}
}

// IDs returns every id ref carries, sorted and unique, in canonical spellings.
func IDs(ref *api.AdvisoryRef) []string {
	if ref == nil {
		return nil
	}
	normalized := *ref
	Normalize(&normalized)
	return sortedUnique(collect(&normalized))
}

// BuildAdvisoryRef normalizes a record id and its aliases into advisory
// identity, or returns nil when no id survives normalization.
func BuildAdvisoryRef(id string, aliases ...string) *api.AdvisoryRef {
	ref := &api.AdvisoryRef{Aliases: append([]string{id}, aliases...)}
	Normalize(ref)
	if ref.OSVID == "" {
		return nil
	}
	return ref
}

// RuleID names the host rule for an advisory finding, or "" without identity.
func RuleID(ref *api.AdvisoryRef) string {
	if ref == nil || ref.OSVID == "" {
		return ""
	}
	return "osv:" + ref.OSVID
}

func collect(ref *api.AdvisoryRef) []string {
	ids := make([]string, 0, 1+len(ref.CVEIDs)+len(ref.GHSAIDs)+len(ref.Aliases))
	ids = append(ids, ref.OSVID)
	ids = append(ids, ref.CVEIDs...)
	ids = append(ids, ref.GHSAIDs...)
	ids = append(ids, ref.Aliases...)
	return ids
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	slices.Sort(values)
	return slices.Compact(values)
}
