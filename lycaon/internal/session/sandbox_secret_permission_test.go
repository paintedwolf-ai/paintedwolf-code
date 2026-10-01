package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func sandboxServicePermission() *hitl.SecretPermission {
	recipient := secretmatch.Recipient{ID: "service", Label: "http://localhost:8080", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}
	grant := hitl.ApprovalGrant{ID: "secret", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "chat", ProjectID: "project",
		Predicate:          hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"value"})},
		SecretFingerprints: []string{"value"}, SecretRecipients: []secretmatch.Recipient{recipient}, Witness: hitl.SecretReleaseWitness([]secretmatch.Recipient{recipient})}
	return &hitl.SecretPermission{ConnectPorts: []uint16{8080}, Fingerprints: []secretmatch.SecretFingerprint{"value"},
		Screen: hitl.SecretScreen{Managed: true, SecretNames: []string{"Service password"}, Recipients: []secretmatch.Recipient{recipient}, DestinationID: recipient.ID, DestinationLabel: recipient.Label, DestinationKind: recipient.Kind, OriginKind: secretmatch.OriginField, SourcePath: "arguments"},
		Offers: []hitl.ApprovalGrantOffer{{ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: grant.Scope, Grant: grant, Coverage: "service password at the named origin"}},
	}
}

func TestLocalCapabilityCardsComposeSecretPermission(t *testing.T) {
	permission := sandboxServicePermission()
	loopback, err := (&LoopbackCheckpointBroker{}).buildCard(tools.LoopbackConnectAsk{
		ProjectID: "project", ToolName: "http_request", SecretPermission: permission,
	}, "chat", "chat", []uint16{8080})
	testutil.FailErr(t, "build combined HTTP permission", err)
	combined, err := (&LocalNetworkCheckpointBroker{}).buildCard(tools.LocalNetworkAsk{
		ProjectID: "project", ToolName: "command", ListenPorts: []uint16{8080}, ConnectPorts: []uint16{8080}, SecretPermission: permission,
	}, "chat", "chat")
	testutil.FailErr(t, "build combined process permission", err)
	for _, card := range []sandboxAskCard{loopback, combined} {
		connections := 0
		for _, target := range card.Plan.Subject.Targets {
			if target.Kind == "loopback_connect" {
				connections++
			}
		}
		if connections != 1 {
			t.Fatalf("combined card has %d connection targets, want one", connections)
		}
		selected, _ := card.Plan.Option(card.Plan.RecommendedOptionID)
		want := hitl.ApprovalRungChat
		if selected.Rung != want {
			t.Fatalf("primary=%s want %s", selected.Rung, want)
		}
		if card.Plan.Presentation.Location == nil {
			t.Fatal("compound disclosure lost its recipients")
		}
		for _, option := range card.Plan.Options {
			if !option.Disabled && option.Rung == hitl.ApprovalRungChat {
				grants := 0
				for _, delta := range option.Authority {
					if delta.Kind == hitl.AuthorityLoopbackConnectChat {
						grants++
					}
				}
				if grants != 1 {
					t.Fatalf("combined chat option installs %d connection grants, want one", grants)
				}
			}
			if option.Kind == hitl.ApprovalOptionQuiet && !option.Disabled {
				t.Fatal("a quiet could omit secret review")
			}
		}
	}
}
