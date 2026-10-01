package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestEvidenceShapesVerificationDispatchesViaRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	resolvePath := filepath.Join(root, "lycaon", "internal", "evidence", "handle.go")
	body, err := os.ReadFile(resolvePath)
	testutil.FailErr(t, "read handle.go", err)
	text := string(body)
	if !strings.Contains(text, "VerifyHandle") {
		t.Fatal("ExcerptMatchesHandle must delegate to VerifyHandle")
	}
	if strings.Contains(text, "switch rec.Kind") || strings.Contains(text, "switch rec.Shape") {
		t.Fatal("handle.go must not branch verification on kind or shape")
	}
}

func TestEvidenceShapesDenRenderByShapeNotKind(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	panelPath := filepath.Join(root, "lycaon-den", "src", "components", "citation", "CitationGroundingPanel.tsx")
	body, err := os.ReadFile(panelPath)
	testutil.FailErr(t, "read CitationGroundingPanel.tsx", err)
	text := string(body)
	if strings.Contains(text, "switch (rec().kind") || strings.Contains(text, "switch (props.record.kind") {
		t.Fatal("CitationGroundingPanel must not switch on evidence kind")
	}
	if !strings.Contains(text, "data-shape") {
		t.Fatal("CitationGroundingPanel must dispatch on shape")
	}

	modelPath := filepath.Join(root, "lycaon-den", "src", "chat", "grounding", "evidence-shape-model.ts")
	modelBody, err := os.ReadFile(modelPath)
	testutil.FailErr(t, "read evidence-shape-model.ts", err)
	modelText := string(modelBody)
	if strings.Contains(modelText, "switch (rec.kind") {
		t.Fatal("evidence-shape-model must not switch on kind for render")
	}
	if !strings.Contains(modelText, "switch (shape)") {
		t.Fatal("evidence-shape-model must switch on shape")
	}
}

func TestEvidenceShapesNoHardcodedCheckLabelMapInDen(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	denSrc := filepath.Join(root, "lycaon-den", "src")
	pattern := regexp.MustCompile(`(WORKER_|SYNTH_|INVEST_)[A-Z0-9_]+\s*:\s*['"]`)
	err := filepath.Walk(denSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		if strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.tsx") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if pattern.Find(body) != nil {
			t.Errorf("%s: hardcoded grounding check-label map forbidden — use EvidenceMeta.ui_label from wire", path)
		}
		return nil
	})
	testutil.FailErr(t, "walk lycaon-den/src", err)
}
