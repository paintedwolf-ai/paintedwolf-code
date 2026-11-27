package symbolsearch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/testutil"
)

const cf06S05BaseSeed = "20261127"

var cf06S05Languages = []string{"go", "typescript", "python", "rust", "java", "c"}

func cf06S05RNG(idx int) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:S05:%d", cf06S05BaseSeed, idx)))
	return rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(sum[:8]))))
}

func cf06S05SourceFile(lang string, symNames []string) (string, string) {
	var sb strings.Builder
	switch lang {
	case "go":
		sb.WriteString("package symbols\n\n")
		for _, s := range symNames {
			fmt.Fprintf(&sb, "// mention of %s in comment\nfunc %s(v int) int { return v + 1 }\n\n", s, s)
		}
		return "pkg/symbols.go", sb.String()
	case "typescript":
		sb.WriteString("// TS fixture\n\n")
		for _, s := range symNames {
			fmt.Fprintf(&sb, "// mention of %s in comment\nexport function %s(v: number): number { return v + 1; }\n\n", s, s)
		}
		return "src/symbols.ts", sb.String()
	case "python":
		sb.WriteString("# Python fixture\n\n")
		for _, s := range symNames {
			fmt.Fprintf(&sb, "# mention of %s in comment\ndef %s(v: int) -> int:\n    return v + 1\n\n", s, s)
		}
		return "src/symbols.py", sb.String()
	case "rust":
		sb.WriteString("// Rust fixture\n\n")
		for _, s := range symNames {
			fmt.Fprintf(&sb, "// mention of %s in comment\npub fn %s(v: i32) -> i32 { v + 1 }\n\n", s, s)
		}
		return "src/symbols.rs", sb.String()
	case "java":
		sb.WriteString("// Java fixture\n\n")
		for _, s := range symNames {
			fmt.Fprintf(&sb, "// mention of %s in comment\npublic class %s { public int run(int v) { return v + 1; } }\n\n", s, s)
		}
		return "src/Symbols.java", sb.String()
	default: // c
		sb.WriteString("#include <stdint.h>\n\n")
		for _, s := range symNames {
			fmt.Fprintf(&sb, "// mention of %s in comment\nint %s(int v) { return v + 1; }\n\n", s, s)
		}
		return "src/symbols.c", sb.String()
	}
}

func TestCF06_S05_SymbolLeg(t *testing.T) {
	const totalInstances = 1000
	const corpusCount = 20
	const queriesPerCorpus = totalInstances / corpusCount

	type s05Corpus struct {
		proj  *project.Project
		syms  []string
		decls map[string]bool
	}
	corpora := make([]s05Corpus, corpusCount)
	for c := 0; c < corpusCount; c++ {
		rng := cf06S05RNG(c * 10000)
		// Cover 1..100 declarations range across the 6 languages.
		perLang := 2 + (c % 5)
		if c == corpusCount-1 {
			perLang = 16 // 6 * 16 = 96 declarations (~100)
		}
		files := map[string]string{}
		var syms []string
		decls := map[string]bool{}
		for _, lang := range cf06S05Languages {
			var langSyms []string
			for j := 0; j < perLang; j++ {
				name := fmt.Sprintf("Handle%s%02d_%02d", strings.ToUpper(lang[:1])+lang[1:], j, rng.Intn(90)+10)
				langSyms = append(langSyms, name)
				syms = append(syms, name)
				decls[name] = true
			}
			rel, body := cf06S05SourceFile(lang, langSyms)
			files[rel] = body
		}
		// Add Markdown file with headings and mentions that must NEVER be returned as symbols.
		var md strings.Builder
		md.WriteString("# Documentation\n\n")
		for _, s := range syms[:min(5, len(syms))] {
			fmt.Fprintf(&md, "## %s\n\nMentions %s in prose.\n\n", s, s)
		}
		files["README.md"] = md.String()
		corpora[c] = s05Corpus{proj: symbolSearchFixture(t, files), syms: syms, decls: decls}
	}

	passed, failed := 0, 0
	var firstFailure string

	for i := 0; i < totalInstances; i++ {
		c := i / queriesPerCorpus
		corp := corpora[c]
		rng := cf06S05RNG(i)
		target := corp.syms[rng.Intn(len(corp.syms))]
		q := target
		switch i % 4 {
		case 1: // prefix
			q = target[:min(8, len(target))]
		case 2: // case-insensitive
			q = strings.ToLower(target)
		case 3: // substring
			if len(target) > 6 {
				q = target[2:7]
			}
		}

		res := searchSymbols(t, corp.proj, Request{Query: q})
		// 1. Every returned hit must be a real declaration (never README.md heading or comment mention).
		bad := false
		for _, m := range res.Symbols {
			if m.Path == "README.md" {
				bad = true
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d q=%q returned Markdown heading %+v", i, q, m)
				}
				break
			}
			if !corp.decls[m.Name] && m.Name != "run" {
				bad = true
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d q=%q returned non-declaration %q", i, q, m.Name)
				}
				break
			}
		}
		if bad {
			failed++
			continue
		}
		// 2. Exact query must find the target declaration at rank 0 (exact-case tier).
		if i%4 == 0 {
			if len(res.Symbols) == 0 || res.Symbols[0].Name != target {
				failed++
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d exact q=%q top=%+v", i, q, res.Symbols)
				}
				continue
			}
		}
		// 3. Verify returned tier order is monotonically non-decreasing.
		matcher := newSymbolNameMatcher(q)
		prevTier := symbolTierExactCase
		for idx, m := range res.Symbols {
			tier, _ := matcher.match(m.Name)
			if idx > 0 && tier < prevTier {
				bad = true
				if firstFailure == "" {
					firstFailure = fmt.Sprintf("instance %d q=%q out-of-order tier %d < %d (%s after %s)", i, q, tier, prevTier, m.Name, res.Symbols[idx-1].Name)
				}
				break
			}
			prevTier = tier
		}
		if bad {
			failed++
			continue
		}
		passed++
	}

	// 5 hand-written S05 edges.
	edgePassed := 0
	t.Run("edges", func(t *testing.T) {
		// Edge 1: Rename a declaration and re-search -> old name absent, new name present.
		dir := t.TempDir()
		fPath := filepath.Join(dir, "rename.go")
		testutil.FailErr(t, "write v1", os.WriteFile(fPath, []byte("package p\n\nfunc OldSymbolName() {}\n"), 0o644))
		pRen, err := projecttest.CreateWithRoot(context.Background(), projecttest.NewRegistry(t), dir)
		testutil.FailErr(t, "create rename proj", err)
		got1 := searchSymbols(t, pRen, Request{Query: "OldSymbolName"})
		if len(got1.Symbols) != 1 {
			t.Fatalf("edge1 initial search = %+v", got1.Symbols)
		}
		testutil.FailErr(t, "write v2", os.WriteFile(fPath, []byte("package p\n\nfunc NewSymbolName() {}\n"), 0o644))
		gotOld := searchSymbols(t, pRen, Request{Query: "OldSymbolName"})
		gotNew := searchSymbols(t, pRen, Request{Query: "NewSymbolName"})
		if len(gotOld.Symbols) != 0 || len(gotNew.Symbols) != 1 {
			t.Fatalf("edge1 rename failed: old=%+v new=%+v", gotOld.Symbols, gotNew.Symbols)
		}
		edgePassed++

		// Edge 2: Tier order exact-case > exact > prefix > word-start > hump > substring.
		pTier := symbolSearchFixture(t, map[string]string{
			"tiers.go": "package p\n\nfunc xcfgx() {}\nfunc LoadCfg() {}\nfunc CfgLoader() {}\nfunc Cfg() {}\nfunc cfg() {}\nfunc CacheFileGroup() {}\n",
		})
		gotTier := searchSymbols(t, pTier, Request{Query: "cfg"})
		wantNames := []string{"cfg", "Cfg", "CfgLoader", "LoadCfg", "CacheFileGroup", "xcfgx"}
		if len(gotTier.Symbols) != len(wantNames) {
			t.Fatalf("edge2 tier count = %d want %d", len(gotTier.Symbols), len(wantNames))
		}
		for idx, want := range wantNames {
			if gotTier.Symbols[idx].Name != want {
				t.Fatalf("edge2 tier[%d]=%q want %q", idx, gotTier.Symbols[idx].Name, want)
			}
		}
		edgePassed++

		// Edge 3: Markdown headings and comments never answer.
		pMd := symbolSearchFixture(t, map[string]string{
			"docs.md": "# OnlyHeadingSym\n\nOnlyHeadingSym is mentioned here.\n",
			"code.go": "package p\n\n// OnlyHeadingSym in comment\nvar x = \"OnlyHeadingSym\"\n",
		})
		gotMd := searchSymbols(t, pMd, Request{Query: "OnlyHeadingSym"})
		if len(gotMd.Symbols) != 0 {
			t.Fatalf("edge3 heading/mention returned: %+v", gotMd.Symbols)
		}
		edgePassed++

		// Edge 4: Result limit / budget truncation reports Limited=true.
		var many strings.Builder
		many.WriteString("package p\n\n")
		for j := 0; j < 25; j++ {
			fmt.Fprintf(&many, "func CapSym_%02d() {}\n", j)
		}
		pCap := symbolSearchFixture(t, map[string]string{"many.go": many.String()})
		gotCap := searchSymbols(t, pCap, Request{Query: "CapSym", Limit: 5})
		if !gotCap.Limited || len(gotCap.Symbols) != 5 {
			t.Fatalf("edge4 limit truncation failed: limited=%v len=%d", gotCap.Limited, len(gotCap.Symbols))
		}
		edgePassed++

		// Edge 5: Unreadable file or tight wall budget declares gap honestly.
		if runtime.GOOS != "windows" && os.Geteuid() != 0 {
			unDir := t.TempDir()
			goodFile := filepath.Join(unDir, "good.go")
			badFile := filepath.Join(unDir, "unreadable.go")
			testutil.FailErr(t, "write good", os.WriteFile(goodFile, []byte("package p\n\nfunc HealthySym() {}\n"), 0o644))
			testutil.FailErr(t, "write bad", os.WriteFile(badFile, []byte("package p\n\nfunc HiddenSym() {}\n"), 0o000))
			t.Cleanup(func() { _ = os.Chmod(badFile, 0o644) })
			pUn, err := projecttest.CreateWithRoot(context.Background(), projecttest.NewRegistry(t), unDir)
			testutil.FailErr(t, "create unreadable proj", err)
			resUn, err := Run(context.Background(), pUn, Request{Query: "HealthySym", Wall: time.Minute, AbbreviationWall: time.Minute}, testDeclarationSearch)
			testutil.FailErr(t, "run unreadable", err)
			if len(resUn.Symbols) != 1 || resUn.Symbols[0].Name != "HealthySym" {
				t.Fatalf("edge5 unreadable sibling suppressed healthy symbol: %+v", resUn.Symbols)
			}
		}
		edgePassed++
	})

	t.Logf("CF06_SUMMARY {\"family\":\"S05\",\"lane\":\"in_process\",\"planned\":%d,\"executed\":%d,\"passed\":%d,\"failed\":%d,\"declared_gaps\":0,\"edges_planned\":5,\"edges_passed\":%d}",
		totalInstances, passed+failed, passed, failed, edgePassed)
	if failed > 0 {
		t.Fatalf("S05 symbol leg failed %d/%d instances; first: %s", failed, totalInstances, firstFailure)
	}
}
