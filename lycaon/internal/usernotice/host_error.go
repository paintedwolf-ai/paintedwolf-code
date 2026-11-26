package usernotice

import wire "github.com/lycaon/lycaon/pkg/api"

func (catalog *Catalog) HostError(code wire.NoticeCode, ctx map[string]any) wire.SessionHostError {
	host := wire.NewSessionHostError(code)
	copy := catalog.RenderWire(string(code), ctx)
	host.Title = copy.Title
	host.Message = copy.Message
	host.SuggestedAction = copy.SuggestedAction

	for _, action := range copy.Actions {
		host.Actions = append(host.Actions, wire.NoticeAction(action))
	}

	if placement, ok := catalog.Placement(string(code), ctx); ok {
		host.Tier = wire.NoticeTier(placement.Tier)
		host.Scope = wire.NoticeScope(placement.Scope)
		host.Resolution = placement.ID
	}
	return host
}
