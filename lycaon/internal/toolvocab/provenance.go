package toolvocab

import "strings"

// Provenance answers which pack authored a piece of catalog content. An empty
// pack id means bundled. A nil field answers "" for everything, so an unset
// Provenance reads as one authority. Remedy reachability is judged per author:
// a narrower profile from another pack is not the rule author's error.
type Provenance struct {
	ToolSchema  func(tool string) string
	ToolProfile func(profileID string) string
	Rule        func(ruleID string) string
}

func (p Provenance) schemaPack(tool string) string {
	if p.ToolSchema == nil {
		return ""
	}
	return strings.TrimSpace(p.ToolSchema(tool))
}

func (p Provenance) profilePack(profileID string) string {
	if p.ToolProfile == nil {
		return ""
	}
	return strings.TrimSpace(p.ToolProfile(profileID))
}

func (p Provenance) rulePack(ruleID string) string {
	if p.Rule == nil {
		return ""
	}
	return strings.TrimSpace(p.Rule(ruleID))
}

// sameAuthority reports whether one author wrote both the rule and the profile,
// which is when an unreachable remedy is a defect.
func (p Provenance) sameAuthority(ruleID, profileID string) bool {
	return p.rulePack(ruleID) == p.profilePack(profileID)
}
