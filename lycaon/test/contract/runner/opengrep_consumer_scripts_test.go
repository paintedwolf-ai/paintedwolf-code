package contract

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenGrepConsumerModesUseExplicitArtifactOwner(t *testing.T) {
	for _, mode := range []string{"--finalize-consumer", "--audit-consumer"} {
		t.Run(mode, func(t *testing.T) {
			f := newScannerScriptFixture(t)
			root := filepath.Join(f.root, "final root")
			args := []string{mode, "-artifact-directory", "/original producer", "-evidence-root", "/source config", "-audit-output", "/audit.json"}
			selected, suffix := "finalize-consumer", "|-root|"+root+"|-consumer-recipe|/recipe with spaces.json"
			if mode == "--audit-consumer" {
				selected, suffix = "audit", "|-packaged-directory|"+root+"|-consumer-identity-file|/identity.txt"
				args = append(args, "-packaged-directory", root, "-consumer-identity-file", "/identity.txt")
			} else {
				args = append(args, "-root", root, "-consumer-recipe", "/recipe with spaces.json")
			}
			_, err := f.run(t, "resolve-opengrep.sh", args)
			if err != nil {
				t.Fatal(err)
			}
			calls := f.calls(t)
			want := "run|./cmd/opengrep-artifact|-mode|" + selected + "|-artifact-directory|/original producer|-evidence-root|/source config|-audit-output|/audit.json" + suffix
			if strings.Count(calls, "go|") != 1 || !strings.Contains(calls, want) || strings.Contains(calls, "|-mode|fetch|") {
				t.Fatalf("consumer mode did not preserve explicit build inputs: %s", calls)
			}
		})
	}
}

func TestOpenGrepConsumerModesRefuseOverrideAuthority(t *testing.T) {
	for _, extra := range [][]string{{"OPENGREP=/candidate"}, {"OPENGREP_SHA256=digest"}, {"LYCAON_OPENGREP_CANDIDATE=/candidate"}} {
		f := newScannerScriptFixture(t)
		if _, err := f.run(t, "resolve-opengrep.sh", []string{"--finalize-consumer"}, extra...); err == nil || f.calls(t) != "" {
			t.Fatal("development override reached the consumer build owner")
		}
	}
	for _, argument := range []string{"-mode", "--mode=fetch"} {
		f := newScannerScriptFixture(t)
		if _, err := f.run(t, "resolve-opengrep.sh", []string{"--audit-consumer", argument, "fetch"}); err == nil || f.calls(t) != "" {
			t.Fatal("consumer mode override reached a tool")
		}
	}
}
