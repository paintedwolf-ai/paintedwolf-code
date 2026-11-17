package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

type grantIDInputs struct {
	scope        hitl.ApprovalGrantScope
	category     string
	pattern      string
	chat         string
	project      string
	witness      hitl.ApprovalGrantWitness
	exactActions []string
}

func (in grantIDInputs) id() string {
	return hitl.ApprovalGrantID(in.scope, in.category, in.pattern, in.chat, in.project, in.witness, in.exactActions)
}

// A durable grant id is stable for one authority and separates every
// dimension of it, so a grant for one project never matches another.
func TestApprovalGrantIDSeparatesEveryAuthorityDimension(t *testing.T) {
	t.Parallel()
	base := grantIDInputs{
		scope: hitl.ApprovalGrantScopeProject, category: "write_root", pattern: "/tmp/cache", project: "proj-1",
		witness:      hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy, RootsDigest: "roots-a"},
		exactActions: []string{"action-a"},
	}
	if base.id() != base.id() {
		t.Fatal("grant id is not stable for one authority")
	}
	variants := map[string]func(*grantIDInputs){
		"project":       func(in *grantIDInputs) { in.project = "proj-2" },
		"scope":         func(in *grantIDInputs) { in.scope = hitl.ApprovalGrantScopeDevice },
		"category":      func(in *grantIDInputs) { in.category = "read_root" },
		"pattern":       func(in *grantIDInputs) { in.pattern = "/tmp/other" },
		"chat":          func(in *grantIDInputs) { in.chat = "chat-1" },
		"fs jail":       func(in *grantIDInputs) { in.witness.FSJailed = false },
		"egress":        func(in *grantIDInputs) { in.witness.Egress = "" },
		"roots digest":  func(in *grantIDInputs) { in.witness.RootsDigest = "roots-b" },
		"exact actions": func(in *grantIDInputs) { in.exactActions = []string{"action-a", "action-b"} },
	}
	seen := map[string]string{base.id(): "base"}
	for name, change := range variants {
		in := base
		in.exactActions = append([]string(nil), base.exactActions...)
		change(&in)
		id := in.id()
		if prior, ok := seen[id]; ok {
			t.Errorf("changing %s produced the same grant id as %s", name, prior)
		}
		seen[id] = name
	}
}
