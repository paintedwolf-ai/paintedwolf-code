package grammargen

import (
	"os"
	"testing"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestFortranTargetedParserParity(t *testing.T) {
	if os.Getenv("GTS_GRAMMARGEN_FORTRAN_TARGETED_PARITY") != "1" {
		t.Skip("set GTS_GRAMMARGEN_FORTRAN_TARGETED_PARITY=1")
	}
	if os.Getenv("GOT_LALR_LR0_CORE_BUDGET") == "" {
		t.Setenv("GOT_LALR_LR0_CORE_BUDGET", "160000000")
	}

	lang, err := generateWithTimeout(FortranGrammar(), 15*time.Minute)
	if err != nil {
		t.Fatalf("generate fortran: %v", err)
	}
	ref := grammars.FortranLanguage()
	adaptExternalScanner(ref, lang)

	tests := []struct {
		name string
		src  string
	}{
		{
			name: "preprocessor define inside if",
			src:  "#if !defined(__GNUC__)\n#define foo\n#endif\n",
		},
		{
			name: "keyword as array identifier assignment",
			src:  "program test\n   data(1, 2) = data(2, 1)\nend program\n",
		},
		{
			name: "program implicit none",
			src:  "PROGRAM TEST\n  implicit none\nEND PROGRAM\n",
		},
		{
			name: "program variable declaration",
			src:  "PROGRAM TEST\n  implicit none\n  integer :: x, y, i, j\nEND PROGRAM\n",
		},
		{
			name: "program inline if assignment",
			src:  "PROGRAM TEST\n  implicit none\n  integer :: x, y, i, j\n  character(len=5) :: arg\n  IF (x < 7) y = 9\nEND PROGRAM\n",
		},
		{
			name: "dimension colon extents",
			src:  "program test\n  real(eb), allocatable, dimension(:, :, :) :: vals\nend program\n",
		},
		{
			name: "function call operands in math expression",
			src:  "program test\n  x = A(i,j) + B(k,l,m)\nend program\n",
		},
		{
			name: "intrinsic calls in division expression",
			src:  "program test\n  x = epsilon(1.0d0)/epsilon(1.0)\nend program\n",
		},
		{
			name: "null literal expression",
			src:  "program test\n  x = null()\nend program\n",
		},
		{
			name: "open statement with keyword argument",
			src:  "program test\n  do i = 1, 10\n    OPEN(i, FILE=\"qwerty\")\n  end do\nend program\n",
		},
		{
			name: "empty single quoted string assignment",
			src:  "program test\n  sngl_qt = ''\nend program\n",
		},
		{
			name: "nonempty single quoted string assignment",
			src:  "program test\n  sngl_qt = '123'\nend program\n",
		},
		{
			name: "empty double quoted string assignment",
			src:  "program test\n  dble_qt = \"\"\nend program\n",
		},
		{
			name: "string concatenation expression",
			src:  "program test\n  val = \"one\"//'two'//sngl_qt//dble_qt\nend program\n",
		},
		{
			name: "identifier kind string literal",
			src:  "program test\n  with_kind = ck_\"string with kind\"\nend program\n",
		},
		{
			name: "numeric kind string literal",
			src:  "program test\n  with_numeral_kind = 4_\"string with kind\"\nend program\n",
		},
		{
			name: "select case with preprocessor cases",
			src:  "program case_preprocessor\n    implicit none\n    character(len=256) :: multidata_item\n    integer :: dim\n    multidata_item = \"test\"\n\n    select case (multidata_item)\n#ifdef UM_PHYSICS\n        case ('plant_func_types')\n                dim = 1\n#endif\n#if TEST > 0\n        case ('test')\n                dim = 1\n#endif\n        case default\n#ifdef HAVE_DEFAULT_BODY\n                dim = 1\n#endif\n    end select\n\nend program\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			genParser := gotreesitter.NewParser(lang)
			refParser := gotreesitter.NewParser(ref)
			if os.Getenv("GTS_GRAMMARGEN_FORTRAN_TARGETED_TRACE") == "1" {
				gotreesitter.DebugDFA.Store(true)
				defer gotreesitter.DebugDFA.Store(false)
				genParser.SetLogger(func(kind gotreesitter.ParserLogType, msg string) {
					if kind == gotreesitter.ParserLogLex {
						t.Logf("GEN lex: %s", msg)
					}
				})
				refParser.SetLogger(func(kind gotreesitter.ParserLogType, msg string) {
					if kind == gotreesitter.ParserLogLex {
						t.Logf("REF lex: %s", msg)
					}
				})
			}
			genTree, _ := genParser.Parse([]byte(tc.src))
			refTree, _ := refParser.Parse([]byte(tc.src))
			genRoot := genTree.RootNode()
			refRoot := refTree.RootNode()
			if genRoot.SExpr(lang) != refRoot.SExpr(ref) {
				t.Fatalf("SExpr mismatch\ngen: %s\nref: %s", genRoot.SExpr(lang), refRoot.SExpr(ref))
			}
			if divs := compareTreesDeep(genRoot, lang, refRoot, ref, "root", 10); len(divs) > 0 {
				t.Fatalf("deep parity mismatch: %v", divs)
			}
		})
	}
}
