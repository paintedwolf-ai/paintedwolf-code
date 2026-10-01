package gotreesitter_test

import (
	"sync"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestTypeScriptMergeCapSeamRaceProbe parses TypeScript on several goroutines
// while another goroutine toggles the variant-B merge-policy test seam. It
// exists to prove the seam global is race-free: with a plain bool global the
// parse path read races the toggle write, and `go test -race` fails; with
// atomic.Bool it is clean. The assertion is only that no goroutine panics or
// is flagged by the race detector, not any specific tree.
func TestTypeScriptMergeCapSeamRaceProbe(t *testing.T) {
	// Restore the seam to its default after the probe, regardless of the last
	// concurrent toggle, so later tests observe the production default.
	defer gotreesitter.SetTypeScriptCapOneStructurePreferenceForTests(false)()

	lang := grammars.TypescriptLanguage()
	srcs := [][]byte{
		[]byte("function f(a = 1) {}\n"),
		[]byte("const f = (a: A): B => { return a; };\n"),
		[]byte("const x = arr.map<T>((v) => v);\n"),
	}

	const rounds = 200
	var wg sync.WaitGroup

	// Togglers: flip the atomic seam repeatedly.
	for tg := 0; tg < 2; tg++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				gotreesitter.SetTypeScriptCapOneStructurePreferenceForTests(i%3 == 0)
			}
		}()
	}

	// Parsers: parse TypeScript concurrently while the seams flip.
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(pi int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				parser := gotreesitter.NewParser(lang)
				tree, err := parser.Parse(srcs[(pi+i)%len(srcs)])
				if err != nil {
					t.Errorf("parse error: %v", err)
					return
				}
				if tree == nil || tree.RootNode() == nil {
					t.Errorf("nil tree")
					return
				}
				tree.Release()
			}
		}(p)
	}

	wg.Wait()
}
