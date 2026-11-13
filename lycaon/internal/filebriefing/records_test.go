package filebriefing

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

const recordedSummary = "- Purpose: Builds the binary\n- Structure: One entry point\n- Key behavior: Exits on failure"

func TestCompletedSummaryIsRecordedWithItsMaterial(t *testing.T) {
	dir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dir, "briefings.db"))
	rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	cfg, err := LoadConfig()
	testutil.FailErr(t, "load config", err)
	records := briefingstore.NewStore(database, dir)
	enabled := &memoryEnablement{}
	enabled.enabled.Store(true)
	service := NewService(t.Context(), Dependencies{Config: cfg, Store: NewSQL(database), Settings: enabled, Records: records,
		Generator: generationFunc(func(context.Context, GenerationRequest, func(string)) (string, error) { return recordedSummary, nil })})
	t.Cleanup(func() {
		service.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		service.Wait(ctx)
	})
	target := serviceTarget("main.go")
	target.ProjectID, target.RootID = testdbseed.DefaultProjectID, rootID
	_, err = service.Request(t.Context(), target, "manual")
	testutil.FailErr(t, "request", err)
	service.jobs.scheduler.Wait(t.Context())

	var recordID string
	testutil.FailErr(t, "read record id", database.QueryRowContext(t.Context(), `SELECT record_id FROM file_briefings WHERE path = 'main.go'`).Scan(&recordID))
	record, err := records.GetRecord(t.Context(), testdbseed.DefaultProjectID, recordID)
	testutil.FailErr(t, "record", err)
	if record.ProfileID != ProfileID || record.Method != "model" || len(record.Sections) != 1 || record.Origin.Trigger != "manual" {
		t.Fatalf("record %+v", record)
	}
	hash := record.Sections[0].ManifestHash
	results, err := records.GetResults(t.Context(), testdbseed.DefaultProjectID, []string{hash})
	testutil.FailErr(t, "results", err)
	if results[hash].Text != recordedSummary {
		t.Fatalf("recorded text %q", results[hash].Text)
	}
	display, err := records.ReadManifest(t.Context(), testdbseed.DefaultProjectID, hash)
	testutil.FailErr(t, "manifest", err)
	for _, supplied := range []string{target.Input.SourceSHA256, "func Build"} {
		if !strings.Contains(string(display), supplied) {
			t.Fatalf("manifest lost %q: %s", supplied, display)
		}
	}

	testutil.FailErr(t, "disable", service.SetEnabled(t.Context(), false))
	if _, err := records.GetRecord(t.Context(), testdbseed.DefaultProjectID, recordID); err == nil {
		t.Fatal("disabling summaries kept their records")
	}
}

func TestManifestTracksSourceRevisionAndPrompt(t *testing.T) {
	cfg, err := LoadConfig()
	testutil.FailErr(t, "load config", err)
	material, err := BuildMaterial(t.Context(), serviceTarget("main.go").Input, cfg)
	testutil.FailErr(t, "material", err)
	changed := serviceTarget("main.go").Input
	changed.SourceSHA256 = "other"
	other, err := BuildMaterial(t.Context(), changed, cfg)
	testutil.FailErr(t, "changed material", err)
	first, err := material.Manifest.Hash()
	testutil.FailErr(t, "hash", err)
	second, err := other.Manifest.Hash()
	testutil.FailErr(t, "hash changed", err)
	if first == second {
		t.Fatal("a new source revision kept the old manifest")
	}
	if material.Manifest.Contract.MaxRunes != cfg.Generation.MaxOutputChars || material.Manifest.Items[2].Text != material.Prompt {
		t.Fatalf("manifest does not describe the submitted prompt: %+v", material.Manifest.Contract)
	}
}

func TestResponseReferencesItsRecordOnlyWhenRecorded(t *testing.T) {
	if Response(Briefing{ProjectID: "p"}).EvidenceRef != nil {
		t.Fatal("an unrecorded summary claimed evidence")
	}
	ref := Response(Briefing{ProjectID: "p", RecordID: "r"}).EvidenceRef
	if ref == nil || ref.ProjectID != "p" || ref.RecordID != "r" || fileBriefingEvent(Briefing{ProjectID: "p", RecordID: "r"}, "complete").EvidenceRef == nil {
		t.Fatalf("evidence %+v", ref)
	}
}
