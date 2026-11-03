package commandconformance

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

const (
	GeneratedProgramsPerSeed = 16
	MaxGeneratedPrograms     = 64
	MaxGeneratedSeeds        = 64
	generatedKinds           = 16
)

// GeneratedSeeds is the recorded CI seed set. Callers receive an independent slice.
func GeneratedSeeds() []uint64 { return []uint64{7, 41, 20261028} }

// GeneratedProgram retains its grammar so reductions never splice shell source.
type GeneratedProgram struct {
	Seed       uint64
	Ordinal    int
	fragments  []generatedFragment
	references []referenceuserland.ID
}

type generatedFragment struct {
	kind   int
	slot   int
	text   string
	number int
}

// Generate produces at most four fragments per program, with loop bounds at most
// four and no recursive calls or unbounded producers.
func Generate(seed uint64, count int) ([]GeneratedProgram, error) {
	if count < 1 || count > MaxGeneratedPrograms {
		return nil, fmt.Errorf("program count must be between 1 and %d", MaxGeneratedPrograms)
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	words := []string{"", "alpha", "two words", "a'b", "$value", "a*b", "a\tb", "x\\y"}
	programs := make([]GeneratedProgram, count)
	for i := range programs {
		p := GeneratedProgram{Seed: seed, Ordinal: i, references: slices.Clone(allUserlands)}
		for j, size := 0, 1+rng.IntN(4); j < size; j++ {
			kind := (i + int(seed%generatedKinds)) % generatedKinds
			if j > 0 {
				kind = rng.IntN(generatedKinds)
			}
			p.fragments = append(p.fragments, generatedFragment{kind: kind, slot: j, text: words[rng.IntN(len(words))], number: 1 + rng.IntN(4)})
			if kind == 3 || kind == 6 {
				p.references = slices.Clone(bashUserlands)
			}
		}
		programs[i] = p
	}
	return programs, nil
}

func shellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Case uses the same workspace and userland applicability as ordinary corpus cases.
func (p GeneratedProgram) Case() Case {
	c := Case{ID: fmt.Sprintf("generated-%d-%d", p.Seed, p.Ordinal), Description: "Seeded bounded shell program", References: slices.Clone(p.references)}
	var source strings.Builder
	for _, f := range p.fragments {
		fmt.Fprintln(&source, f.source(f.slot))
		if f.kind == 13 {
			c.Tracked = append(c.Tracked, fmt.Sprintf("generated-%d.txt", f.slot))
		}
	}
	if source.Len() == 0 {
		source.WriteString(":\n")
	}
	c.Program = source.String()
	return c
}

func (f generatedFragment) source(index int) string {
	q, n := shellLiteral(f.text), f.number
	switch f.kind {
	case 0:
		//nolint:gocritic // Literal shell quotes wrap escaped shell text.
		return fmt.Sprintf("printf '%%s|%%s\\n' %s \"%s\"", q, strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`").Replace(f.text))
	case 1:
		return fmt.Sprintf("v=%s; printf '%%s|%%s|%%s\\n' \"${v:-fallback}\" \"${v:+set}\" \"${#v}\"", q)
	case 2:
		return fmt.Sprintf("n=%d; printf '%%s\\n' \"$(( (n + 2) * 3 - n ))\"", n)
	case 3:
		return fmt.Sprintf("printf '%%s\\n' item{a,b}{1..%d}", n)
	case 4:
		return fmt.Sprintf("cat <<'GENERATED_END'\n%s\nGENERATED_END", f.text)
	case 5:
		return fmt.Sprintf("v=%s; cat <<GENERATED_END\n${v}\nGENERATED_END", q)
	case 6:
		return fmt.Sprintf("v=%s; cat <<< \"$v\"", q)
	case 7:
		return fmt.Sprintf("for v in %s x; do printf '%%s\\n' \"$v\"; done", q)
	case 8:
		return fmt.Sprintf("i=0; while [ \"$i\" -lt %d ]; do printf '%%s\\n' \"$i\"; i=$((i + 1)); done", n)
	case 9:
		return fmt.Sprintf("i=0; until [ \"$i\" -ge %d ]; do printf '%%s\\n' \"$i\"; i=$((i + 1)); done", n)
	case 10:
		return fmt.Sprintf("if [ -n %s ]; then printf 'yes\\n'; else printf 'no\\n'; fi", q)
	case 11:
		return fmt.Sprintf("generated_fn() { printf '%%s|%%s\\n' \"$1\" \"$#\"; }; generated_fn %s %d", q, n)
	case 12:
		return fmt.Sprintf("printf '%%s\\n' %s alpha alpha | sort | uniq | tr 'a-z' 'A-Z'", q)
	case 13:
		name := fmt.Sprintf("generated-%d.txt", index)
		return fmt.Sprintf("printf '%%s\\n' %s > %s; printf '%%s\\n' %d >> %s; cat < %s", q, name, n, name, name)
	case 14:
		return "test -f notes.txt && test -d sub; basename sub/a.txt; dirname sub/a.txt; wc -c < empty.txt"
	default:
		return fmt.Sprintf("printf '%%s\\n' %s alpha beta | sed 's/alpha/first/g' | cut -c 1-%d", q, n)
	}
}

// complexity decreases on every fragment deletion or scalar reduction.
func (p GeneratedProgram) complexity() int {
	weight := 0
	for _, f := range p.fragments {
		weight += 1 + len(f.text) + f.number
	}
	return weight
}

// ReductionCandidates are strictly smaller grammar-preserving programs.
func (p GeneratedProgram) ReductionCandidates() []GeneratedProgram {
	var candidates []GeneratedProgram
	for i := range p.fragments {
		c := p
		c.fragments = slices.Delete(slices.Clone(p.fragments), i, i+1)
		candidates = append(candidates, c)
	}
	for i, f := range p.fragments {
		if f.text != "" {
			c := p
			c.fragments = slices.Clone(p.fragments)
			c.fragments[i].text = ""
			candidates = append(candidates, c)
		}
		if f.number > 1 {
			c := p
			c.fragments = slices.Clone(p.fragments)
			c.fragments[i].number = 1
			candidates = append(candidates, c)
		}
	}
	seen := map[string]bool{p.Case().Program: true}
	candidates = slices.DeleteFunc(candidates, func(candidate GeneratedProgram) bool {
		source := candidate.Case().Program
		if seen[source] {
			return true
		}
		seen[source] = true
		return false
	})
	slices.SortStableFunc(candidates, func(a, b GeneratedProgram) int { return a.complexity() - b.complexity() })
	return candidates
}
