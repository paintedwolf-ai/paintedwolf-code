package hitl

import (
	"slices"
	"testing"
)

func TestSecretServiceConnectionAddsOnlyUncoveredPorts(t *testing.T) {
	ports := []uint16{8080, 9090}
	for _, test := range []struct {
		name      string
		authority []ApprovalAuthorityDelta
		want      []uint16
	}{
		{name: "no connection authority", want: ports},
		{name: "chat connections", authority: []ApprovalAuthorityDelta{{Kind: AuthorityLoopbackConnectChat, ChatSessionID: "chat"}}},
		{name: "partial ports", authority: []ApprovalAuthorityDelta{{Kind: AuthorityLoopbackConnectChat, ChatSessionID: "chat", ConnectPorts: []uint16{8080}}}, want: []uint16{9090}},
		{name: "other chat", authority: []ApprovalAuthorityDelta{{Kind: AuthorityLoopbackConnectChat, ChatSessionID: "other"}}, want: ports},
		{name: "listener only", authority: []ApprovalAuthorityDelta{{Kind: AuthorityLocalListenChat, ChatSessionID: "chat"}}, want: ports},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := uncoveredServicePorts(ports, test.authority, "chat"); !slices.Equal(got, test.want) {
				t.Fatalf("additional ports = %v, want %v", got, test.want)
			}
			if !slices.Equal(ports, []uint16{8080, 9090}) {
				t.Fatal("connection composition changed its input ports")
			}
		})
	}
}
