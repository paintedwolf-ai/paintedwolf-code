package main

import "testing"

func TestDevelopmentHistoryPolicy(t *testing.T) {
	t.Parallel()
	cfg, err := loadConfig("../..", ".commentlint.json")
	if err != nil {
		t.Fatalf("load repository policy: %v", err)
	}
	rules, err := compileForbidden(configForbidden(cfg.Forbid))
	if err != nil {
		t.Fatalf("compile repository policy: %v", err)
	}
	for _, tc := range []struct {
		text     string
		rejected bool
	}{
		{"// Added in PR #123 to replace the old path.", true},
		{"// As of commit abc123, this uses the shared store.", true},
		{"// Format 1 stores released display paths.", false},
		{"// Recover a committed journal entry before retrying.", false},
		{"// Keep the migration reader for published databases.", false},
	} {
		got := inspect("fixture.go", comment{Text: tc.text}, options{forbidden: rules})
		if (len(got) > 0) != tc.rejected {
			t.Errorf("%q: findings = %+v", tc.text, got)
		}
	}
}
