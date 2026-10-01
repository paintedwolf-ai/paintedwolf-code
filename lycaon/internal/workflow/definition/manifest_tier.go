package definition

import (
	"fmt"
	"sort"
	"strings"
)

// IsCatalogVisible reports whether a manifest may appear in product catalog APIs.
// Ambient-attach workflows (attach.policy session_create) are excluded; everything
// else is catalog-visible.
func (m Manifest) IsCatalogVisible() bool {
	return m.Attach.Policy != AttachPolicySessionCreate
}

// DefaultAmbientRef identifies the workflow attached at session create.
type DefaultAmbientRef struct {
	ID      string
	Version string
}

// ValidateAmbientAttachBijection ensures exactly one resolved manifest declares
// attach.policy session_create, and when ambient is non-zero that manifest matches
// default_ambient_workflow from the registry.
func ValidateAmbientAttachBijection(manifests map[string]Manifest, ambient DefaultAmbientRef) error {
	var ambientKeys []string
	var ambientManifest Manifest
	for _, m := range manifests {
		if m.Attach.Policy != AttachPolicySessionCreate {
			continue
		}
		key := ManifestKey(m.ID, m.Version)
		ambientKeys = append(ambientKeys, key)
		ambientManifest = m
	}
	sort.Strings(ambientKeys)
	if len(ambientKeys) != 1 {
		if len(ambientKeys) == 0 {
			return fmt.Errorf("exactly one manifest must have attach.policy session_create; found none")
		}
		return fmt.Errorf("exactly one manifest must have attach.policy session_create; found %d: %s",
			len(ambientKeys), strings.Join(ambientKeys, ", "))
	}
	if strings.TrimSpace(ambient.ID) == "" && strings.TrimSpace(ambient.Version) == "" {
		return nil
	}
	if ambientManifest.ID != ambient.ID || ambientManifest.Version != ambient.Version {
		return fmt.Errorf("session_create manifest %s@%s does not match default_ambient_workflow %s@%s",
			ambientManifest.ID, ambientManifest.Version, ambient.ID, ambient.Version)
	}
	return nil
}

// validateUniqueSessionCreateAttach rejects multiple session_create attaches after resolve.
func validateUniqueSessionCreateAttach(manifests map[string]Manifest) error {
	var keys []string
	for _, m := range manifests {
		if m.Attach.Policy == AttachPolicySessionCreate {
			keys = append(keys, ManifestKey(m.ID, m.Version))
		}
	}
	if len(keys) <= 1 {
		return nil
	}
	sort.Strings(keys)
	return fmt.Errorf("multiple manifests have attach.policy session_create: %s", strings.Join(keys, ", "))
}
