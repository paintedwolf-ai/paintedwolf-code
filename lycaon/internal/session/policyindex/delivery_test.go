package policyindex

import (
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestManifestContainsOnlyIntactDeliveredPolicyBlocks(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root instructions")
	delivery, err := m.Resolve(t.Context(), sess, []agentpolicy.Scope{{RootID: root.ID, RootPath: root.Path, Path: "."}})
	testutil.FailErr(t, "policy delivery", err)
	message := delivery.Messages[0]
	if !m.ManifestFor(transcript.Project(delivery.Messages)).Covers(delivery.Manifest) {
		t.Fatal("authority projection lost intact policy delivery")
	}
	for name, change := range map[string]func(*api.Message){
		"content":   func(m *api.Message) { m.Content += " changed" },
		"identity":  func(m *api.Message) { m.ID = "unregistered" },
		"origin":    func(m *api.Message) { m.Origin = api.MessageOriginModel },
		"authority": func(m *api.Message) { m.Authority = api.ContentAuthorityNone },
		"trust":     func(m *api.Message) { m.TrustTier = api.ContentTrustTierUntrusted },
		"role":      func(m *api.Message) { m.Role = api.MessageRoleUser },
		"parts":     func(m *api.Message) { m.ContentParts = []api.MessageContentPart{{Content: "other content"}} },
	} {
		t.Run(name, func(t *testing.T) {
			altered := message
			change(&altered)
			if m.ManifestFor([]api.Message{altered}).Covers(delivery.Manifest) {
				t.Fatal("altered policy counted as delivered")
			}
		})
	}
	if m.ManifestFor(nil).Covers(delivery.Manifest) {
		t.Fatal("omitted policy counted as delivered")
	}
	prior := m.ManifestFor(delivery.Messages)
	writePolicy(t, root.Path, "AGENTS.md", "New root instructions")
	later, err := m.Resolve(t.Context(), sess, nil)
	testutil.FailErr(t, "later policy delivery", err)
	if prior.Covers(later.Manifest) {
		t.Fatal("earlier request borrowed a later request's policy")
	}
}
