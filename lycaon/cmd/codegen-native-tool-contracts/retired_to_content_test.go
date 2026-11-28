package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderedContractsListRetiredToContentToolsInOrder(t *testing.T) {
	out, err := renderToolContracts(nativemanifest.Config{RetiredToContentContract: map[string]string{
		"zeta_tool": "fixture.zeta", "alpha_tool": "fixture.alpha",
	}}, nil)
	testutil.FailErr(t, "render tool contracts", err)
	text := string(out)
	alpha, zeta := strings.Index(text, `"alpha_tool": "fixture.alpha"`), strings.Index(text, `"zeta_tool": "fixture.zeta"`)
	if alpha < 0 || zeta < alpha {
		t.Fatalf("retired entries missing or out of order:\n%s", text)
	}
}

func TestRenderedContractsCarryEachRecordedSuccessor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retirements.json")
	testutil.FailErr(t, "write retirements", os.WriteFile(path, []byte(`{"vocabularies":{"tools":{
		"zeta_tool":["alpha_tool","beta_tool"],
		"old_note":{"content_contract":"fixture.note","version":1}}}}`), 0o600))
	retirements, err := loadToolRetirements(path)
	testutil.FailErr(t, "load retirements", err)
	out, err := renderToolContracts(nativemanifest.Config{}, retirements)
	testutil.FailErr(t, "render tool contracts", err)
	text := string(out)
	note := strings.Index(text, `"old_note": {ContentContract: "fixture.note"}`)
	zeta := strings.Index(text, `"zeta_tool": {Successors: []string{"alpha_tool", "beta_tool"}}`)
	if note < 0 || zeta < note {
		t.Fatalf("retirements missing or out of order:\n%s", text)
	}
}

func TestRetirementsWithoutSuccessorAreRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retirements.json")
	testutil.FailErr(t, "write retirements", os.WriteFile(path, []byte(`{"vocabularies":{"tools":{"gone":[]}}}`), 0o600))
	if _, err := loadToolRetirements(path); err == nil {
		t.Fatal("a retirement with no successor was accepted")
	}
}
