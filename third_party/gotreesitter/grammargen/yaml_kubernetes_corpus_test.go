package grammargen

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

type yamlKubernetesCorpusFixture struct {
	name   string
	sha256 string
}

var yamlKubernetesCorpus = []yamlKubernetesCorpusFixture{
	{name: "csi-hostpath-testing.yaml", sha256: "08c25e2ecfd3b82831c567199927146a3f12cb006a4d012c0917c02886a1f1cc"},
	{name: "etcd-statefulset.yaml", sha256: "f681976cc6c79bd3337fbc4144ee86fbd226e602fed6d0414b91a6935e0fb8a9"},
}

func TestYAMLOwnedGrammarKubernetesCorpusParity(t *testing.T) {
	genLang, refLang := loadGeneratedYAMLLanguagesForParity(t)
	root := filepath.Join("testdata", "yaml", "kubernetes")
	for _, fixture := range yamlKubernetesCorpus {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, fixture.name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			sum := sha256.Sum256(src)
			if got := hex.EncodeToString(sum[:]); got != fixture.sha256 {
				t.Fatalf("fixture hash = %s, want %s", got, fixture.sha256)
			}
			assertGeneratedAndReferenceDeepParity(t, genLang, refLang, string(src))
		})
	}
}
