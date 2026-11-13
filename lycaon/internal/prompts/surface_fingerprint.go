package prompts

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// CacheFingerprint identifies the rendered catalog and machine facts.
func (surface AgentPromptSurface) CacheFingerprint() string {
	var catalog []AgentSkillView
	if surface.FullCatalog {
		catalog = surface.Skills
	}
	payload, err := json.Marshal(struct {
		HasSkills             bool
		FullCatalog           bool
		Skills                []AgentSkillView
		HostResources         []AgentHostResourceView
		HostResourcesOmitted  int
		SessionScratchDir     string
		ReviewsConfinedWrites bool
		CommandExternalHidden bool
	}{
		len(surface.Skills) > 0,
		surface.FullCatalog,
		catalog,
		surface.HostResources,
		surface.HostResourcesOmitted,
		surface.SessionScratchDir,
		surface.Posture.ReviewsConfinedWrites(),
		surface.CommandExternalHidden,
	})
	if err != nil {
		return surface.Fingerprint
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
