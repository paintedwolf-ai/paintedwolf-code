package definition

import (
	"strings"
)

// AttachPolicy describes when a manifest is attached to a session.
type AttachPolicy string

const (
	AttachPolicySessionCreate AttachPolicy = "session_create"
)

func parseAttachPolicy(raw string) AttachPolicy {
	return AttachPolicy(strings.TrimSpace(raw))
}

func (p AttachPolicy) valid() bool {
	switch p {
	case "", AttachPolicySessionCreate:
		return true
	default:
		return false
	}
}

// ManifestAttach holds attach metadata from YAML.
type ManifestAttach struct {
	Policy AttachPolicy
}
