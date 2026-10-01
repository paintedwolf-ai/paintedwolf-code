package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestDevelopmentProjectsVerifyDeclaredSourceProvenance(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*projectCase)
	}{
		{name: "original bytes"},
		{name: "normalized newlines", change: func(p *projectCase) { p.Files["app.py"] = "pass\n" }},
		{name: "unbound source", change: func(p *projectCase) { p.Files["extra.py"] = "pass\n" }},
		{name: "missing digest", change: func(p *projectCase) { delete(p.Provenance.SourceSHA256, "app.py") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			const source = "pass\r\n"
			project := projectCase{
				Name: "ordinary", Language: "python", Framework: "flask", ReviewStatus: "pending",
				Adjudication: "Development source used for rule tuning; whole-project review is incomplete.",
				Files:        map[string]string{"app.py": source},
				Provenance: &projectProvenance{
					Origin: "https://example.com/ordinary", Revision: "0123456789abcdef0123456789abcdef01234567",
					License: "MIT", SamplingUnit: "ordinary", Adjudicator: "source reviewer", UsedForTuning: true,
					SourceSHA256: map[string]string{"app.py": fmt.Sprintf("%x", sha256.Sum256([]byte(source)))},
				},
			}
			if scenario.change != nil {
				scenario.change(&project)
			}
			raw, err := yaml.Marshal(projectCorpus{Partition: "development", Projects: []projectCase{project}})
			testutil.FailErr(t, "encode development corpus", err)
			path := filepath.Join(t.TempDir(), "projects.yaml")
			testutil.FailErr(t, "write development corpus", os.WriteFile(path, raw, 0o600))
			_, err = loadProjects(path)
			if scenario.change == nil {
				testutil.FailErr(t, "load original development source", err)
			} else if err == nil {
				t.Fatal("changed development source retained its declared provenance")
			}
		})
	}
}
