package secretmint

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/testutil"
)

type fixtureFile struct {
	Mint   []fixtureCase `yaml:"mint"`
	Use    []fixtureCase `yaml:"use"`
	Attack []fixtureCase `yaml:"attack"`
}

type fixtureCase struct {
	Tool       string         `yaml:"tool"`
	Args       map[string]any `yaml:"args"`
	Assignment string         `yaml:"assignment"`
}

func TestCatalogFixturesMintUseAttack(t *testing.T) {
	ins := testInspector(t)
	raw, err := config.Read(config.SecretMintFixtures)
	testutil.FailErr(t, "read fixtures", err)
	var file fixtureFile
	testutil.FailErr(t, "parse fixtures", yaml.Unmarshal(raw, &file))
	for i, c := range file.Mint {
		hits := ins.Inspect(c.Tool, c.Args)
		if c.Assignment != "" && !hasAssignment(hits, c.Assignment) {
			t.Errorf("mint[%d] missing assignment %s: got %v", i, c.Assignment, assignments(hits))
		}
		if len(hits) == 0 {
			t.Errorf("mint[%d] expected a hit", i)
		}
	}
	for i, c := range file.Use {
		if hits := ins.Inspect(c.Tool, c.Args); len(hits) != 0 {
			t.Errorf("use[%d] must be silent: assignments=%v", i, assignments(hits))
		}
	}
	for i, c := range file.Attack {
		if hits := ins.Inspect(c.Tool, c.Args); len(hits) != 0 {
			t.Errorf("attack[%d] must be silent: assignments=%v", i, assignments(hits))
		}
	}
}
