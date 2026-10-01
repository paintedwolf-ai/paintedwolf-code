package hostresources

import (
	"slices"
	"testing"
)

func TestSavedAuthorityUsesDeclaredPlatformConnections(t *testing.T) {
	service := &Service{env: discoveryEnvironment{platform: "macos"}, defs: []Definition{
		{ID: "daemon", Realizations: []Realization{
			{Platforms: []string{"linux"}, Connections: []ConnectionTemplate{{Mode: ConnectionDirectIP}}},
			{Platforms: []string{"macos"}, Connections: []ConnectionTemplate{{Mode: ConnectionLocalService}}},
		}},
		{ID: "ordinary", Realizations: []Realization{{Connections: []ConnectionTemplate{{Mode: ConnectionProxy}}}}},
	}}
	if got := service.ConnectionModes([]string{"daemon", "missing"}); !slices.Equal(got, []ConnectionMode{ConnectionLocalService}) {
		t.Fatalf("declared authority = %v", got)
	}
	if got := service.ConnectionModes([]string{"ordinary"}); !slices.Equal(got, []ConnectionMode{ConnectionProxy}) {
		t.Fatalf("ordinary authority = %v", got)
	}
}
