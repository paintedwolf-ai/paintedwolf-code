package commandprogram

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	bb04SeedCount         = 256
	bb04FamilyAGenVersion = "bb04-family-a-shared-shell-semantics-v1"
	bb04FamilyBGenVersion = "bb04-family-b-effect-sequences-v1"
)

func bb04GeneratorDigest(version string) string {
	sum := sha256.Sum256([]byte(version))
	return hex.EncodeToString(sum[:8])
}

// ---------------------------------------------------------------------------

func bb04GenerateFamilyA(seed uint64) []string {
	rng := rand.New(rand.NewPCG(seed, 0xbb04a11ce))
	count := 1 + rng.IntN(8)
	literals := []string{
		`'plain'`,
		`'two words'`,
		`'don'\''t stop'`,
		`'a|b;c&d'`,
		`''`,
		`"quoted $v"`,
	}
	vars := []string{"v", "k", "item"}
	targets := []string{"out_a.txt", "out_b.txt"}
	stmts := make([]string, 0, count)
	stmts = append(stmts, fmt.Sprintf("v=%s", literals[rng.IntN(4)]))
	for len(stmts) < count {
		switch rng.IntN(9) {
		case 0: // quoted literal + apostrophe + spaces
			lit := literals[rng.IntN(len(literals))]
			stmts = append(stmts, fmt.Sprintf("printf '[%%s]\\n' %s", lit))
		case 1: // variable assignment + parameter expansion
			name := vars[rng.IntN(len(vars))]
			lit := literals[rng.IntN(5)]
			stmts = append(stmts, fmt.Sprintf("%s=%s; printf '%%s:%%s\\n' \"${%s:-fallback}\" \"${#%s}\"", name, lit, name, name))
		case 2: // if/elif/else branch with file/string predicate
			if rng.IntN(2) == 0 {
				stmts = append(stmts, `if [ -f seed.txt ]; then printf 'has-seed\n'; else printf 'no-seed\n'; fi`)
			} else {
				stmts = append(stmts, `if [ "$v" = 'plain' ]; then printf 'is-plain\n'; else printf 'other\n'; fi`)
			}
		case 3: // case branch
			stmts = append(stmts, `case "$v" in plain) printf 'c:plain\n';; *stop*) printf 'c:apos\n';; *) printf 'c:other\n';; esac`)
		case 4: // finite for loop with quoted items
			stmts = append(stmts, `for x in 'one' 'two three' 'it'\''s'; do printf 'f:%s\n' "$x"; done`)
		case 5: // finite while loop with arithmetic
			limit := 1 + rng.IntN(3)
			stmts = append(stmts, fmt.Sprintf(`i=0; while [ "$i" -lt %d ]; do printf 'w:%%d\n' "$i"; i=$((i + 1)); done`, limit))
		case 6: // command substitution
			if rng.IntN(2) == 0 {
				stmts = append(stmts, `sub=$(printf 'sub %s' "$v"); printf 's:%s\n' "$sub"`)
			} else {
				stmts = append(stmts, `sub=$(cat seed.txt | grep beta); printf 'g:%s\n' "$sub"`)
			}
		case 7: // pipeline (matching or early-close or transform)
			switch rng.IntN(3) {
			case 0:
				stmts = append(stmts, `cat seed.txt | grep alpha | tr a-z A-Z`)
			case 1:
				stmts = append(stmts, `cat seed.txt | head -n 1`)
			default:
				stmts = append(stmts, `printf 'line1\nline2\n' | grep line2 | cat`)
			}
		case 8: // redirects (replace, append, input, stderr)
			target := targets[rng.IntN(len(targets))]
			switch rng.IntN(4) {
			case 0:
				stmts = append(stmts, fmt.Sprintf(`printf 'r:%%s\n' "$v" > %s`, target))
			case 1:
				stmts = append(stmts, fmt.Sprintf(`printf 'a:%%s\n' "$v" >> %s`, target))
			case 2:
				stmts = append(stmts, `cat < quote.txt | head -n 1`)
			default:
				stmts = append(stmts, `printf 'err-note\n' >&2`)
			}
		}
	}
	return stmts
}

func bb04MinimizeStatements(stmts []string, diverges func(string) bool) string {
	cur := append([]string(nil), stmts...)
	for changed := true; changed && len(cur) > 1; {
		changed = false
		for i := 0; i < len(cur); i++ {
			candidate := make([]string, 0, len(cur)-1)
			candidate = append(candidate, cur[:i]...)
			candidate = append(candidate, cur[i+1:]...)
			if diverges(strings.Join(candidate, "\n")) {
				cur = candidate
				changed = true
				break
			}
		}
	}
	return strings.Join(cur, "\n")
}

func TestBB04GeneratedSharedShellSemanticsDifferential(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available on PATH")
	}
	verOut, err := exec.CommandContext(t.Context(), bashPath, "--version").Output()
	testutil.FailErr(t, "query bash --version", err)
	bashVersion := strings.TrimSpace(strings.Split(string(verOut), "\n")[0])
	digest := bb04GeneratorDigest(bb04FamilyAGenVersion)
	t.Logf("Family A differential: seeds=0..%d generator_digest=%s bash=%q", bb04SeedCount-1, digest, bashVersion)

	type failure struct {
		seed      uint64
		diff      string
		program   string
		minimized string
	}

	workers := min(runtime.GOMAXPROCS(0), 8)
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan uint64, bb04SeedCount)
	for seed := uint64(0); seed < bb04SeedCount; seed++ {
		jobs <- seed
	}
	close(jobs)

	var (
		mu       sync.Mutex
		failures []failure
		wg       sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seed := range jobs {
				stmts := bb04GenerateFamilyA(seed)
				program := strings.Join(stmts, "\n")
				runPair := func(src string) (bool, string) {
					evalDir := t.TempDir()
					bashDir := t.TempDir()
					bb04SeedTree(t, evalDir)
					bb04SeedTree(t, bashDir)
					ev := bb04RunCommandProgram(t, evalDir, src)
					ba := bb04RunBash(t, bashPath, bashDir, src, true)
					return bb04ResultsEqual(ev, ba)
				}
				if ok, diff := runPair(program); !ok {
					minimized := bb04MinimizeStatements(stmts, func(src string) bool {
						ok, _ := runPair(src)
						return !ok
					})
					mu.Lock()
					failures = append(failures, failure{
						seed:      seed,
						diff:      diff,
						program:   program,
						minimized: minimized,
					})
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		sort.Slice(failures, func(i, j int) bool { return failures[i].seed < failures[j].seed })
		first := failures[0]
		t.Fatalf("Family A differential found %d/%d divergences (generator_digest=%s bash=%q); first seed=%d diff=%s\n--- program ---\n%s\n--- minimized ---\n%s",
			len(failures), bb04SeedCount, digest, bashVersion, first.seed, first.diff, first.program, first.minimized)
	}
}

// TestBB04PipefailContractDifferential explicitly pins the documented pipefail
// contract: commandprogram always reports the rightmost nonzero pipeline stage
// (matching bash with `set -o pipefail`), admits `set -o pipefail`, and refuses
// `set +o pipefail` at admission.
func TestBB04PipefailContractDifferential(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available on PATH")
	}
	program := "grep nonexistent seed.txt | cat"
	evalDir, bashOnDir, bashOffDir := t.TempDir(), t.TempDir(), t.TempDir()
	bb04SeedTree(t, evalDir)
	bb04SeedTree(t, bashOnDir)
	bb04SeedTree(t, bashOffDir)

	ev := bb04RunCommandProgram(t, evalDir, program)
	baOn := bb04RunBash(t, bashPath, bashOnDir, program, true)
	baOff := bb04RunBash(t, bashPath, bashOffDir, program, false)

	if ok, diff := bb04ResultsEqual(ev, baOn); !ok {
		t.Fatalf("pipefail-on differential mismatch: %s", diff)
	}
	if ev.status != 1 || baOff.status != 0 {
		t.Fatalf("expected product-defined pipefail contrast: eval=%d bash(pipefail-on)=%d bash(pipefail-off)=%d",
			ev.status, baOn.status, baOff.status)
	}

	// `set -o pipefail` is admitted; `set +o pipefail` is refused.
	if res := bb04RunCommandProgram(t, evalDir, "set -o pipefail; grep nonexistent seed.txt | cat"); res.err != nil || res.status != 1 {
		t.Fatalf("set -o pipefail failed: %+v", res)
	}
	mem := fixtureScratch(t)
	if _, err := commandsurface.ParseAdmitted(t.Context(), mem, []byte("set +o pipefail; echo ok"), commandsurface.ParseOptions{SourceName: "pipefail.pw"}); err == nil {
		t.Fatal("set +o pipefail must be refused by version 1 syntax admission")
	}
}
