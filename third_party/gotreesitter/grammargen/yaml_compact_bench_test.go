package grammargen

import (
	"sync"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

var yamlCompactBenchmarkSource = []byte(`---
service:
  name: parser
  enabled: true
  ports: [8080, 8443]
  labels:
    owner: platform
    tier: critical
workers:
  - name: ingest
    replicas: 3
    limits: { cpu: "500m", memory: "256Mi" }
  - name: index
    replicas: 2
    limits: { cpu: "1", memory: "512Mi" }
notes: >
  This source exercises mappings, sequences, flow collections,
  quoted scalars, and a folded scalar in one parser run.
`)

var yamlCompactBenchmarkLanguages [2]struct {
	once sync.Once
	lang *gotreesitter.Language
	err  error
}

func ownedYAMLBenchmarkLanguage(compact bool) (*gotreesitter.Language, error) {
	index := 0
	if compact {
		index = 1
	}
	cache := &yamlCompactBenchmarkLanguages[index]
	cache.once.Do(func() {
		grammar := YAMLGrammar()
		grammar.CompactParseStates = compact
		cache.lang, cache.err = GenerateLanguage(grammar)
		if cache.err != nil {
			return
		}
		adaptExternalScanner(grammars.YamlLanguage(), cache.lang)
	})
	return cache.lang, cache.err
}

func BenchmarkYAMLOwnedParserBaseline(b *testing.B) {
	benchmarkOwnedYAMLParser(b, false)
}

func BenchmarkYAMLOwnedParserCompact(b *testing.B) {
	benchmarkOwnedYAMLParser(b, true)
}

func benchmarkOwnedYAMLParser(b *testing.B, compact bool) {
	lang, err := ownedYAMLBenchmarkLanguage(compact)
	if err != nil {
		b.Fatalf("generate YAML language: %v", err)
	}
	parser := gotreesitter.NewParser(lang)
	b.ReportAllocs()
	b.SetBytes(int64(len(yamlCompactBenchmarkSource)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree, err := parser.Parse(yamlCompactBenchmarkSource)
		if err != nil {
			b.Fatalf("parse YAML: %v", err)
		}
		tree.Release()
	}
}
