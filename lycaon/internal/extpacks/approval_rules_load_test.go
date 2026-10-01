package extpacks

import "testing"

func TestLoadEffectiveApprovalRules(t *testing.T) {
	eff := approvalRuleCatalog(map[string]string{
		"approvals/rules/ask-release":  "category: command\npattern: git push*\neffect: ask\n",
		"approvals/rules/deny-secrets": "category: path\npattern: .env*\neffect: deny\n",
	})
	rules, diags, err := LoadEffectiveApprovalRules(eff)
	if err != nil {
		t.Fatalf("LoadEffectiveApprovalRules: %v", err)
	}
	if len(diags) != 0 || len(rules) != 2 {
		t.Fatalf("rules=%#v diagnostics=%#v", rules, diags)
	}
	if rules[0].UnitID != "approvals/rules/ask-release" || rules[0].PackID != "acme/policy" {
		t.Fatalf("first rule provenance = %#v", rules[0])
	}
}

func TestLoadEffectiveApprovalRulesRejectsNonAdditiveAndLooseShapes(t *testing.T) {
	for name, body := range map[string]string{
		"allow effect":        "category: command\npattern: git status\neffect: allow\n",
		"unknown category":    "category: posture\npattern: strict\neffect: ask\n",
		"relative write root": "category: write_root\npattern: tmp/cache\neffect: deny\n",
		"unknown field":       "category: tool\npattern: command\neffect: ask\nreuse: task\n",
		"multiple documents":  "category: tool\npattern: command\neffect: ask\n---\ncategory: tool\npattern: read\neffect: deny\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, diags, err := LoadEffectiveApprovalRules(approvalRuleCatalog(map[string]string{
				"approvals/rules/invalid": body,
			}))
			if err == nil || len(diags) != 1 || diags[0].Code != DiagApprovalRuleInvalid {
				t.Fatalf("error=%v diagnostics=%#v", err, diags)
			}
		})
	}
}

func approvalRuleCatalog(units map[string]string) *EffectiveCatalog {
	loaded := make(map[string]UnitEffective, len(units))
	for id, body := range units {
		loaded[id] = UnitEffective{
			ID: id, Kind: "approvals", Status: UnitStatusLoaded,
			WinnerPackID: "acme/policy", Content: []byte(body),
		}
	}
	return &EffectiveCatalog{Loaded: loaded}
}
