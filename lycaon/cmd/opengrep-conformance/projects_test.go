package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectCorpusRejectsUnreviewedAndEscapingCases(t *testing.T) {
	valid := "projects:\n- name: example\n  language: python\n  adjudication: Constant code is safe\n  files:\n    app.py: pass\n  findings: []\n"
	for name, body := range map[string]string{
		"escape":          strings.Replace(valid, "app.py:", "../app.py:", 1),
		"no-adjudication": strings.Replace(valid, "  adjudication: Constant code is safe\n", "", 1),
		"unknown-field":   valid + "  unexpected: true\n",
		"empty":           "projects: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cases.yaml")
			testutil.FailErr(t, "write corpus", os.WriteFile(path, []byte(body), 0o600))
			if _, err := loadProjects(path); err == nil {
				t.Fatal("invalid corpus accepted")
			}
		})
	}
}
