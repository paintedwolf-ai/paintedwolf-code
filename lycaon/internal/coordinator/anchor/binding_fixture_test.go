package anchor_test

import "github.com/lycaon/lycaon/internal/extpacks"

func bindingCatalog(name string, body []byte) *extpacks.EffectiveCatalog {
	id := "host/bindings/" + name
	return &extpacks.EffectiveCatalog{Loaded: map[string]extpacks.UnitEffective{
		id: {ID: id, WinnerPackID: "fixture/anchors", Content: body},
	}}
}
