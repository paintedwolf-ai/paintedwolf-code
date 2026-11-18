package app

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/testutil"
)

// phaseLog records the boot phases Build reaches.
type phaseLog struct{ phases []startupprotocol.Phase }

func (p *phaseLog) Phase(phase startupprotocol.Phase) error {
	p.phases = append(p.phases, phase)
	return nil
}
func (*phaseLog) Ready(int) error     { return nil }
func (*phaseLog) Failed(string) error { return nil }

// A floor pack that does not load completely stops boot at the credentials phase.
func TestCorruptFloorPackStopsBoot(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the app graph up to the credential floors")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	for _, pack := range []string{detectionpack.KeyMaterialPackID, detectionpack.CredentialStorePackID} {
		dir := config.DetectionPacksDir.Join(pack)
		rulesDir := dir.Join(detectionpack.PackRulesDir)
		rules, err := config.List(rulesDir)
		testutil.FailErr(t, "list bundled rules", err)
		if len(rules) == 0 {
			t.Fatalf("floor pack %s ships no rules", pack)
		}
		for name, files := range map[string]map[config.Rel]string{
			"manifest": {dir.Join(detectionpack.PackManifestFile): "id: [unterminated\n"},
			"one rule": {rulesDir.Join(rules[0].Name()): "title: [unterminated\n"},
		} {
			t.Run(pack+"/"+name, func(t *testing.T) {
				configtest.Overlay(t, files)
				cfg := testBuildConfig(t, t.TempDir())
				log := &phaseLog{}
				cfg.Startup = log
				built, err := Build(t.Context(), cfg)
				if err == nil {
					_ = built.Close()
					t.Fatal("Build succeeded with a corrupt floor pack")
				}
				if len(log.phases) == 0 || log.phases[len(log.phases)-1] != startupprotocol.PhaseCredentials {
					t.Fatalf("boot phases = %v, want the last to be %s", log.phases, startupprotocol.PhaseCredentials)
				}
				if slices.Contains(log.phases, startupprotocol.PhaseHostResources) {
					t.Fatalf("boot passed the credential floors: %v", log.phases)
				}
			})
		}
	}
}
