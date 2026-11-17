package contract

import (
	"os"
	"path/filepath"
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
