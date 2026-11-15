package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestMintedKindsTUIFollowsCommandExternalExecReach(t *testing.T) {
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "load binding", err)

	for _, tc := range []struct {
		name string
		held map[string]bool
		want bool
	}{
		{
			name: "empty held set",
			held: map[string]bool{},
			want: false,
		},
		{
			name: "command read only",
			held: map[string]bool{"command": true},
			want: false,
		},
		{
			name: "command with mutation only",
			held: map[string]bool{"command": true, toolcontract.ReachCommandMutation: true},
			want: false,
		},
		{
			name: "command with network egress only",
			held: map[string]bool{"command": true, toolcontract.ReachCommandNetworkEgress: true},
			want: false,
		},
		{
			name: "command with external exec",
			held: map[string]bool{"command": true, toolcontract.ReachCommandExternalExec: true},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			minted, err := b.MintedKinds(tc.held)
			testutil.FailErr(t, "MintedKinds", err)
			if minted["tui"] != tc.want {
				t.Fatalf("MintedKinds(%v)[tui] = %t, want %t", tc.held, minted["tui"], tc.want)
			}
		})
	}
}
