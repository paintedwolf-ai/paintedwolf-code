package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestInventoryWorkflowCoversPolicyInputs(t *testing.T) {
	t.Parallel()
	repo := filepath.Join("..", "..", "..")
	p, err := loadPolicy(filepath.Join(repo, policyDir))
	testutil.FailErr(t, "load dependency policy", err)
	body, err := os.ReadFile(filepath.Join(repo, ".github/workflows/dependency-inventory.yml"))
	testutil.FailErr(t, "read inventory workflow", err)
	var workflow struct {
		On struct {
			Push struct {
				Paths []string `yaml:"paths"`
			} `yaml:"push"`
		} `yaml:"on"`
	}
	testutil.FailErr(t, "decode inventory workflow", yaml.Unmarshal(body, &workflow))
	inputs := map[string]bool{}
	for _, s := range p.sections {
		if m := s.Manifest; m != nil {
			inputs[m.Path] = true
			if m.Lock != "" {
				inputs[m.Lock] = true
			}
		}
		rows := append([]rowConfig(nil), s.Rows...)
		for _, g := range s.Groups {
			rows = append(rows, g.Rows...)
		}
		for _, r := range rows {
			for _, pin := range r.Pins {
				inputs[pin.File] = true
			}
		}
	}
	for input := range inputs {
		covered := false
		for _, trigger := range workflow.On.Push.Paths {
			if trigger == input {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("inventory input %q needs an explicit workflow push path", input)
		}
	}
}
