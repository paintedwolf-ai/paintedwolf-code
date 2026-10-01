package main

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestHeldOutProvenanceBindsOriginalBytesAndIndependentProjects(t *testing.T) {
	source := "pass\r\n"
	project := projectCase{Name: "pinned", Framework: "flask", Families: []string{"rule"}, Files: map[string]string{"app.py": source},
		Provenance: &projectProvenance{Origin: "https://example.com/project", Revision: "0123456789abcdef0123456789abcdef01234567", License: "MIT", SamplingUnit: "project", Adjudicator: "reviewer",
			SourceSHA256: map[string]string{"app.py": fmt.Sprintf("%x", sha256.Sum256([]byte(source)))}}}
	units := map[string]bool{}
	if err := validateHeldOutProject(project, units); err != nil {
		t.Fatalf("valid provenance: %v", err)
	}
	if err := validateHeldOutProject(project, units); err == nil {
		t.Fatal("same project was accepted twice as independent evidence")
	}
	project.Files["app.py"] = "pass\n"
	if err := validateHeldOutProject(project, map[string]bool{}); err == nil {
		t.Fatal("changed source bytes retained held-out provenance")
	}
	project.Files["app.py"] = source
	project.Provenance.UsedForTuning = true
	if err := validateHeldOutProject(project, map[string]bool{}); err == nil {
		t.Fatal("tuned project remained held out")
	}
}

func TestProjectFamiliesCannotNameUnselectedOrCoverageRules(t *testing.T) {
	bundle := []byte("rules:\n- id: rule\n  metadata:\n    cwe: [CWE-78, CWE-88]\n- id: observed\n  metadata:\n    purpose: coverage\n")
	for _, families := range [][]string{{"missing"}, {"observed"}, {"rule", "rule"}} {
		if err := validateProjectFamilies([]projectCase{{Name: "project", Families: families}}, bundle); err == nil {
			t.Fatalf("invalid family list accepted: %v", families)
		}
	}
	if err := validateProjectFamilies([]projectCase{{Name: "project", Families: []string{"rule"}, Findings: []projectFinding{{Rule: "rule"}}}}, bundle); err != nil {
		t.Fatalf("selected family rejected: %v", err)
	}
}
