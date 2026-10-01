package rules

import (
	"bytes"
	"container/list"
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGateCachePreservesInputIdentityAndOutputIsolation(t *testing.T) {
	c := &gateCache{entries: make(map[string]*list.Element)}
	files := []GateRuleFile{{Name: "first", Data: []byte("rules:\n- id: first\n  languages: [go]\n  severity: ERROR\n  pattern: sink(...)\n")}}
	first, err := c.compile(files)
	testutil.FailErr(t, "compile first", err)
	original := bytes.Clone(first)
	first[0] = '!'
	again, err := c.compile(files)
	testutil.FailErr(t, "reuse", err)
	if !bytes.Equal(original, again) {
		t.Fatal("caller mutated cached bundle")
	}
	files[0].Data = bytes.ReplaceAll(files[0].Data, []byte("sink"), []byte("source"))
	changed, err := c.compile(files)
	testutil.FailErr(t, "compile changed bytes", err)
	if bytes.Equal(original, changed) {
		t.Fatal("changed bytes reused old bundle")
	}
	files = append(files, files[0])
	if _, err := c.compile(files); err == nil {
		t.Fatal("duplicate selection bypassed validation")
	}
	files[0].Data = []byte("invalid")
	if _, err := c.compile(files); err == nil {
		t.Fatal("invalid edit reused cached validation")
	}
}

func TestGateCacheConcurrentReuseAndEviction(t *testing.T) {
	c := &gateCache{entries: make(map[string]*list.Element)}
	var workers sync.WaitGroup
	for i := range 24 {
		workers.Go(func() {
			files := []GateRuleFile{{Name: "rule", Data: fmt.Appendf(nil, "rules:\n- id: rule%d\n  languages: [go]\n  severity: ERROR\n  pattern: sink(...)\n", i%12)}}
			_, err := c.compile(files)
			testutil.FailErr(t, "concurrent compile", err)
		})
	}
	workers.Wait()
	if c.order.Len() > 8 || c.bytes > gateCacheBytes {
		t.Fatal("cache exceeded bounds")
	}
}

func BenchmarkGateCatalog(b *testing.B) {
	cfg, err := LoadOpengrepGates()
	if err != nil {
		b.Fatalf("load gates: %v", err)
	}
	paths := append(ActiveVendorGatePaths(cfg), ActiveLycaonGatePaths(cfg)...)
	files := make([]GateRuleFile, 0, len(paths))
	for _, path := range paths {
		raw, readErr := readGateFile(path, "")
		if readErr != nil {
			b.Fatalf("read gate: %v", readErr)
		}
		files = append(files, GateRuleFile{Name: path, Data: raw})
	}
	b.Run("compile", func(b *testing.B) {
		for b.Loop() {
			if _, err := CompileGateRuleFiles(files); err != nil {
				b.Fatalf("compile gates: %v", err)
			}
		}
	})
	b.Run("cached_with_reads", func(b *testing.B) {
		if _, err := CompileGateRules(cfg, ""); err != nil {
			b.Fatalf("warm cache: %v", err)
		}
		for b.Loop() {
			if _, err := CompileGateRules(cfg, ""); err != nil {
				b.Fatalf("reuse gates: %v", err)
			}
		}
	})
}
