package prompts_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPromptLayers_ReadFileWithProvenance_Bundled(t *testing.T) {
	layers := prompts.PromptLayers{}
	body, prov, err := layers.ReadFileWithProvenance("guidance/reject/_reject")
	testutil.FailErr(t, "ReadFileWithProvenance", err)
	if len(body) == 0 {
		t.Fatal("empty body")
	}
	if prov.SourceTier != "bundled" {
		t.Fatalf("SourceTier = %q, want bundled", prov.SourceTier)
	}
	if prov.UnitKind != "guidance" {
		t.Fatalf("UnitKind = %q, want guidance", prov.UnitKind)
	}
	sum := sha256.Sum256(body)
	wantHash := hex.EncodeToString(sum[:])
	if prov.ContentSha256 != wantHash {
		t.Fatalf("ContentSha256 = %q, want %q", prov.ContentSha256, wantHash)
	}
}

func TestPromptLayers_ReadFileWithProvenance_Archive(t *testing.T) {
	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "archive", "1.0.0")
	guidanceDir := filepath.Join(archiveDir, "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customContent := []byte("Archive custom prompt content\n")
	if err := os.WriteFile(filepath.Join(guidanceDir, "coordinator-security-challenge.md"), customContent, 0o644); err != nil {
		t.Fatal(err)
	}

	layers := prompts.PromptLayers{
		WorkflowArchive: archiveDir,
	}
	body, prov, err := layers.ReadFileWithProvenance("guidance/coordinator-security-challenge.md")
	testutil.FailErr(t, "ReadFileWithProvenance archive", err)
	if string(body) != string(customContent) {
		t.Fatalf("got body %q, want %q", string(body), string(customContent))
	}
	if prov.SourceTier != "archive" {
		t.Fatalf("SourceTier = %q, want archive", prov.SourceTier)
	}
	sum := sha256.Sum256(customContent)
	wantHash := hex.EncodeToString(sum[:])
	if prov.ContentSha256 != wantHash {
		t.Fatalf("ContentSha256 = %q, want %q", prov.ContentSha256, wantHash)
	}
}

func TestFileTemplateEngine_RenderWithProvenance(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, provs, err := engine.RenderWithProvenance(context.Background(), "guidance/reject/_reject", map[string]any{
		"code": "TEST", "what": "blocked", "cause": "blocked", "why": "blocked", "fix": "retry", "instead": "retry", "category": "recoverable",
	})
	testutil.FailErr(t, "RenderWithProvenance", err)
	if len(out) == 0 {
		t.Fatal("empty output")
	}
	if len(provs) == 0 {
		t.Fatal("expected provenance records, got none")
	}
	found := false
	for _, p := range provs {
		if p.UnitID == "guidance/reject/_reject" {
			found = true
			if p.SourceTier != "bundled" {
				t.Fatalf("root template tier = %q, want bundled", p.SourceTier)
			}
		}
	}
	if !found {
		t.Fatalf("expected guidance/reject/_reject in provenance records: %+v", provs)
	}
}

func TestFileTemplateEngine_WithProvenanceRecorder(t *testing.T) {
	var recorded []prompts.UnitProvenanceRecord
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}).
		WithProvenanceRecorder(func(ref string, prov []prompts.UnitProvenanceRecord) {
			recorded = append(recorded, prov...)
		})

	_, err := engine.RenderGuidance(context.Background(), "reject/_reject", map[string]any{
		"code": "TEST", "what": "blocked", "cause": "blocked", "why": "blocked", "fix": "retry", "instead": "retry", "category": "recoverable",
	})
	testutil.FailErr(t, "RenderGuidance", err)
	if len(recorded) == 0 {
		t.Fatal("expected recorder to be invoked, got 0 records")
	}
}
