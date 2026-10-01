package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGroundingClassificationContractLocked(t *testing.T) {
	t.Parallel()
	if guidance.GroundingVerdictPrimary != "verbatim_substring" {
		t.Fatalf("GroundingVerdictPrimary = %q", guidance.GroundingVerdictPrimary)
	}
	if guidance.LeakDetectionMode != "observed_set_membership" {
		t.Fatalf("LeakDetectionMode = %q", guidance.LeakDetectionMode)
	}
	if guidance.LeakSeverityReject != "reject" {
		t.Fatalf("LeakSeverityReject = %q", guidance.LeakSeverityReject)
	}
	if guidance.LeakSeverityAdvisory != "advisory" {
		t.Fatalf("LeakSeverityAdvisory = %q", guidance.LeakSeverityAdvisory)
	}
}

func TestLeakDetectorUsesObservedSetNotShapeRegex(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	leakPath := filepath.Join(root, "lycaon", "internal", "guidance", "leak_detector.go")
	src, err := os.ReadFile(leakPath)
	testutil.FailErr(t, "read leak_detector.go", err)
	body := string(src)
	for _, forbidden := range []string{
		"ExtractCitations(",
		"isProsePathLineCitation(",
		"isBarePathCitation(",
		"isDelimitedPathCitation(",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("leak_detector.go must not use shape-regex gate %s", forbidden)
		}
	}
	if !strings.Contains(body, "evidence.ObservedCitations(") {
		t.Fatal("leak_detector.go must iterate evidence.ObservedCitations for membership detection")
	}
}

func TestNoShapeRegexClassifierFeedsLeakVerdict(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	guidanceDir := filepath.Join(root, "lycaon", "internal", "guidance")
	fset := token.NewFileSet()
	var offenders []string
	_ = filepath.Walk(guidanceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := contractcheck.CallFuncName(call.Fun)
			switch name {
			case "ExtractCitations", "isProsePathLineCitation", "isBarePathCitation", "isDelimitedPathCitation":
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+name)
			}
			return true
		})
		return nil
	})
	if len(offenders) > 0 {
		t.Fatalf("shape-regex classifiers must not feed leak/grounding verdicts in production guidance code:\n%s",
			strings.Join(offenders, "\n"))
	}
}
