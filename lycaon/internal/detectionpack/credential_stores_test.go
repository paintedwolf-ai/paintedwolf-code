package detectionpack

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

// Credential-store paths feed the write-deny floor independently of ask severity.
func TestCredentialStorePathsIgnoresAskSeverityFilter(t *testing.T) {
	t.Parallel()
	lowRule := mustRule(t, `title: Overlay low-severity credential store
id: 99999999-9999-4999-8999-999999999999
description: d
logsource: {product: lycaon, service: tool_exec}
level: low
detection:
  sel: {TargetFile|contains: '~/.overlay-secrets/creds.json'}
  condition: sel`)
	m := NewMatcher(&Catalog{Packs: []Pack{
		{ID: CredentialStorePackID, Enabled: true, Rules: []Rule{lowRule}},
	}})

	paths := m.CredentialStorePaths()
	found := false
	for _, p := range paths {
		if p == "~/.overlay-secrets/creds.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CredentialStorePaths() = %v, want it to include the low-severity overlay rule's TargetFile", paths)
	}

	// Low-severity rules without a mint tag remain inert for Match.
	ev := NewEvent(ActionObservation{
		Tool:        "command",
		TargetFiles: []string{"~/.overlay-secrets/creds.json"},
		SessionID:   "s",
		Boundary:    hitl.Contained{FSJailed: true, Egress: "proxy"},
	})
	if _, ok := m.Match(ev); ok {
		t.Fatalf("a level: low rule with no mint tag must stay inert for the ask path")
	}
}
