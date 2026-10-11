package toolhost

import (
	"github.com/lycaon/lycaon/internal/toolprofiles"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/webresearch"
)

// WebServices owns web provider discovery and runtime visibility.
type WebServices struct {
	directDiscovererFactory webresearch.DirectDiscovererFactory
	policy                  *toolprofiles.ProfilePolicyEngine
	boundary                *sandbox.Boundary
}

func (r *WebServices) SetDirectDiscovererFactory(factory webresearch.DirectDiscovererFactory) {
	if r == nil {
		return
	}
	r.directDiscovererFactory = factory
}

func (r *WebServices) SetWebResearchConfig(cfg *webresearch.ConfigStore) {
	if r == nil {
		return
	}
	if r.policy == nil {
		r.policy = toolprofiles.NewProfilePolicyEngine(r.boundary)
	}
	r.policy.SetRuntimeToolDeny(webresearch.SearchToolRuntimeDeny(cfg))
}

func (r *WebServices) DirectFactoryGetter() webresearch.FactoryGetter {
	if r == nil || r.directDiscovererFactory == nil {
		return nil
	}
	factory := r.directDiscovererFactory
	return func() webresearch.DirectDiscovererFactory { return factory }
}
