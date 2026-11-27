package portablepython

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkloadDependencyDiffOracle(t *testing.T) {
	t.Parallel()
	beforeLock := []byte(`
[[package]]
name = "huggingface-hub"
version = "2.0.0"
[[package]]
name = "openai"
version = "3.19.2"
[[package]]
name = "ruff"
version = "0.16.9"
`)
	afterLock := []byte(`
[[package]]
name = "huggingface-hub"
version = "2.2.0"
[[package]]
name = "openai"
version = "3.35.1"
[[package]]
name = "ruff"
version = "0.17.1"
`)
	beforeReq := []byte("laya==0.3.20\ntorch==2.14.0\nnumpy==2.5.3\nsafetensors==0.8.0\ntokenizers==0.23.2\ntransformers==5.17.0\nhuggingface_hub==2.0.0\n")
	afterReq := []byte("laya==0.4.11\ntorch==2.14.1\nnumpy==2.5.3\nsafetensors==0.8.0\ntokenizers==0.23.3\ntransformers==5.20.1\nhuggingface_hub==2.2.0\n")

	exp, err := ComputeDepDiff(beforeLock, afterLock, beforeReq, afterReq)
	contractcheck.FailErr(t, "compute dep diff", err)
	if len(exp.Rows) != 8 {
		t.Fatalf("expected 8 diff rows (3 lock + 5 req), got %d", len(exp.Rows))
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"source", "package", "old_version", "new_version"})
	for _, r := range exp.Rows {
		_ = w.Write([]string{r.Source, r.Package, r.OldVersion, r.NewVersion})
	}
	w.Flush()
	contractcheck.FailErr(t, "grade valid dep diff", GradeDependencyDiff(exp, buf.Bytes(), map[string][]string{"before": nil, "after": nil}))

	var lockOnly bytes.Buffer
	wl := csv.NewWriter(&lockOnly)
	_ = wl.Write([]string{"source", "package", "old_version", "new_version"})
	for _, r := range exp.Rows[:3] {
		_ = wl.Write([]string{r.Source, r.Package, r.OldVersion, r.NewVersion})
	}
	wl.Flush()
	if err := GradeDependencyDiff(exp, lockOnly.Bytes(), map[string][]string{"before": nil, "after": nil}); err == nil {
		t.Fatal("expected lock-only 3-row CSV to fail")
	}

	afterReqDrift := bytes.Replace(afterReq, []byte("huggingface_hub==2.2.0"), []byte("huggingface_hub==2.1.0"), 1)
	expDrift, err := ComputeDepDiff(beforeLock, afterLock, beforeReq, afterReqDrift)
	contractcheck.FailErr(t, "compute drift dep diff", err)
	if len(expDrift.Discrepancies["after"]) != 1 || expDrift.Discrepancies["after"][0] != "huggingface-hub" {
		t.Fatalf("expected huggingface-hub discrepancy, got %v", expDrift.Discrepancies["after"])
	}
}

func buildCustomXLSX(t *testing.T, name1 string, rows1 [][]string, pane1 string, name2 string, rows2 [][]string, pane2 string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeEntry := func(name, body string) {
		f, err := zw.Create(name)
		contractcheck.FailErr(t, "create xlsx entry "+name, err)
		_, err = f.Write([]byte(body))
		contractcheck.FailErr(t, "write xlsx entry "+name, err)
	}
	writeEntry("xl/workbook.xml", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="%s" sheetId="1" r:id="rId1"/>
    <sheet name="%s" sheetId="2" r:id="rId2"/>
  </sheets>
</workbook>`, name1, name2))
	writeEntry("xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
</Relationships>`)
	renderSheet := func(rows [][]string, paneXML string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
		if paneXML != "" {
			fmt.Fprintf(&b, `<sheetViews><sheetView workbookViewId="0">%s</sheetView></sheetViews>`, paneXML)
		}
		b.WriteString(`<sheetData>`)
		for _, r := range rows {
			b.WriteString(`<row>`)
			for _, cell := range r {
				fmt.Fprintf(&b, `<c t="inlineStr"><is><t>%s</t></is></c>`, cell)
			}
			b.WriteString(`</row>`)
		}
		b.WriteString(`</sheetData></worksheet>`)
		return b.String()
	}
	writeEntry("xl/worksheets/sheet1.xml", renderSheet(rows1, pane1))
	writeEntry("xl/worksheets/sheet2.xml", renderSheet(rows2, pane2))
	contractcheck.FailErr(t, "close xlsx zip", zw.Close())
	return buf.Bytes()
}

func TestWorkloadManifestCSVAndXLSXOracle(t *testing.T) {
	t.Parallel()
	manifest := []byte(`repos:
  - {name: cobra, language: go, split: train, spdx: Apache-2.0, commit: c1, license_files: {LICENSE: d1}}
  - {name: flask, language: python, split: train, spdx: BSD-3-Clause, commit: c2, license_files: {LICENSE: d2}}
  - {name: zod, language: typescript, split: holdout, spdx: MIT, commit: c3, license_files: {LICENSE: d3}}
  - {name: ripgrep, language: rust, split: holdout, spdx: MIT, commit: c4, license_files: {LICENSE-MIT: d4, COPYING: d5}}
`)
	goodCSV := []byte("repository,language,spdx,commit,license_path,expected_sha256\n" +
		"zod,typescript,MIT,c3,LICENSE,d3\n" +
		"ripgrep,rust,MIT,c4,LICENSE-MIT,d4\n" +
		"ripgrep,rust,MIT,c4,COPYING,d5\n")
	contractcheck.FailErr(t, "grade valid holdout CSV", GradeLicenseCSV(manifest, goodCSV))

	repoRows := [][]string{
		{"Name", "Language", "Split", "License", "Pinned Commit"},
		{"cobra", "go", "train", "Apache-2.0", "c1"},
		{"flask", "python", "train", "BSD-3-Clause", "c2"},
		{"zod", "typescript", "holdout", "MIT", "c3"},
		{"ripgrep", "rust", "holdout", "MIT", "c4"},
	}
	countRows := [][]string{
		{"Split", "go", "python", "rust", "typescript", "Total"},
		{"holdout", "0", "0", "1", "1", "2"},
		{"train", "1", "1", "0", "0", "2"},
		{"Total", "1", "1", "1", "1", "4"},
	}
	paneA2 := `<pane ySplit="1" topLeftCell="A2" state="frozen"/>`
	paneB2 := `<pane xSplit="1" ySplit="1" topLeftCell="B2" state="frozen"/>`
	paneB1ColOnly := `<pane xSplit="1" topLeftCell="B1" state="frozen"/>`

	goodXLSX := buildCustomXLSX(t, "Repositories", repoRows, paneA2, "Counts by Split and Language", countRows, paneB2)
	contractcheck.FailErr(t, "grade frozen-replay workbook", GradeRepoWorkbook(manifest, goodXLSX))

	// Column-only freeze (B1, no header row frozen) must fail.
	colOnlyXLSX := buildCustomXLSX(t, "Repositories", repoRows, paneA2, "Counts by Split and Language", countRows, paneB1ColOnly)
	if err := GradeRepoWorkbook(manifest, colOnlyXLSX); err == nil {
		t.Fatal("expected column-only freeze (B1) to fail header-freeze check")
	}

	// Live deliverable mode accepts alternate sheet titles, alternate row order, and tall count table.
	reorderedRepos := [][]string{repoRows[0], repoRows[4], repoRows[2], repoRows[3], repoRows[1]}
	tallCounts := [][]string{
		{"Split", "Language", "Count"},
		{"train", "go", "1"},
		{"train", "python", "1"},
		{"holdout", "typescript", "1"},
		{"holdout", "rust", "1"},
	}
	liveXLSX := buildCustomXLSX(t, "Repo Roster", reorderedRepos, `<pane topLeftCell="A2" state="frozen"/>`, "Split Summary", tallCounts, paneA2)
	contractcheck.FailErr(t, "grade live workbook with tall counts", GradeRepoWorkbookWithMode(manifest, liveXLSX, WorkbookLiveDeliverable))
	if err := GradeRepoWorkbook(manifest, liveXLSX); err == nil {
		t.Fatal("expected strict frozen-replay mode to reject alternate sheet titles/layout")
	}
}

func buildReleaseZip(t *testing.T, anchors map[string][]byte, order []string, flatten, includeSums, dupEntry bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var sums strings.Builder
	for _, name := range order {
		data := anchors[name]
		member := "releases/" + name
		if flatten {
			member = name
		}
		f, err := zw.Create(member)
		contractcheck.FailErr(t, "create member", err)
		_, err = f.Write(data)
		contractcheck.FailErr(t, "write member", err)
		h := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), member)
	}
	if dupEntry {
		f, err := zw.Create("releases/" + order[0])
		contractcheck.FailErr(t, "create duplicate member", err)
		_, err = f.Write(anchors[order[0]])
		contractcheck.FailErr(t, "write duplicate member", err)
	}
	if includeSums {
		f, err := zw.Create("SHA256SUMS")
		contractcheck.FailErr(t, "create SHA256SUMS", err)
		_, err = f.Write([]byte(sums.String()))
		contractcheck.FailErr(t, "write SHA256SUMS", err)
	}
	contractcheck.FailErr(t, "close zip", zw.Close())
	return buf.Bytes()
}

func TestWorkloadReleaseArchiveAndIndexOracle(t *testing.T) {
	t.Parallel()
	anchors := map[string][]byte{
		"a.json": []byte(`{"kind":"dataset","version":"v1","sha256sums":"aaa"}`),
		"b.json": []byte(`{"kind":"heads","version":"v1","sha256sums":"bbb"}`),
	}
	indexJSON := []byte(`{"schema":"pw-decide-release-index/1","versions":{"v1":{"dataset":{"sha256sums":"aaa"},"heads":{"sha256sums":"bbb"}}}}`)
	contractcheck.FailErr(t, "grade release index", GradeReleaseIndex(anchors, indexJSON))

	// Negative control: extra unexpected version/kind row must fail.
	extraIndex := []byte(`{"schema":"pw-decide-release-index/1","versions":{"v1":{"dataset":{"sha256sums":"aaa"},"heads":{"sha256sums":"bbb"}},"v2":{"heads":{"sha256sums":"ccc"}}}}`)
	if err := GradeReleaseIndex(anchors, extraIndex); err == nil {
		t.Fatal("expected extra release-index version/kind row to fail")
	}

	good := buildReleaseZip(t, anchors, []string{"a.json", "b.json"}, false, true, false)
	contractcheck.FailErr(t, "grade reproducible archive", GradeReleaseArchive(anchors, good, good))

	// Positive control: alternate valid deterministic SHA256SUMS line order passes.
	altOrder := buildReleaseZip(t, anchors, []string{"b.json", "a.json"}, false, true, false)
	contractcheck.FailErr(t, "grade alternate SHA256SUMS line order", GradeReleaseArchive(anchors, altOrder, altOrder))

	// Negative controls: duplicate ZIP member, flattened paths, missing SHA256SUMS.
	dupZip := buildReleaseZip(t, anchors, []string{"a.json", "b.json"}, false, true, true)
	if err := GradeReleaseArchive(anchors, dupZip, dupZip); err == nil {
		t.Fatal("expected duplicate ZIP entry name to fail")
	}
	flat := buildReleaseZip(t, anchors, []string{"a.json", "b.json"}, true, true, false)
	if err := GradeReleaseArchive(anchors, flat, flat); err == nil {
		t.Fatal("expected flattened archive paths to fail even when reproducible")
	}
	noSums := buildReleaseZip(t, anchors, []string{"a.json", "b.json"}, false, false, false)
	if err := GradeReleaseArchive(anchors, noSums, noSums); err == nil {
		t.Fatal("expected archive without root SHA256SUMS to fail")
	}
}

func TestWorkloadMarkdownLinksProvenanceMathAndSplitOracles(t *testing.T) {
	t.Parallel()

	// 1. Relative link audit with subfolder encoded path + fragment/web exclusions.
	mdFiles := map[string]string{
		"README.md":           "[v](docs/verify.md) [m](MISSING.md) [w](https://example.com) [f](#sec)",
		"docs/verify.md":      "ok",
		"docs/guide/index.md": "[enc](../my%20doc.md#top) [web](http://x.test) [frag](#only)",
		"docs/my doc.md":      "encoded target",
	}
	goodLinksCSV := []byte("source,target,target_exists\n" +
		"README.md,docs/verify.md,True\n" +
		"README.md,MISSING.md,False\n" +
		"docs/guide/index.md,../my%20doc.md,True\n")
	contractcheck.FailErr(t, "grade markdown links", GradeMarkdownLinks(mdFiles, goodLinksCSV))
	badLinksCSV := append(goodLinksCSV, []byte("README.md,https://example.com,False\n")...)
	if err := GradeMarkdownLinks(mdFiles, badLinksCSV); err == nil {
		t.Fatal("expected CSV including web link to fail")
	}

	// 2. Streamed provenance: >2 chunks with nonempty tail + bounded reads.
	provFiles := map[string][]byte{
		"a.bin": bytes.Repeat([]byte("A"), 20),
		"b.bin": []byte("short"),
	}
	wantHash := ComputeStreamedProvenanceHash(provFiles)
	contractcheck.FailErr(t, "grade bounded streamed provenance", GradeStreamedProvenance(provFiles, StreamedProvenanceObs{
		CandidateSHA256: wantHash,
		ReadChunkSizes:  []int{8, 8, 4, 5},
		MaxChunkSize:    8,
	}))
	if err := GradeStreamedProvenance(provFiles, StreamedProvenanceObs{
		CandidateSHA256: wantHash,
		ReadChunkSizes:  []int{20, 5},
		MaxChunkSize:    8,
	}); err == nil {
		t.Fatal("expected unbounded read size to fail streamed provenance")
	}

	// 3. Project math & unittest discovery + negative controls.
	goodFail := UnittestRunObs{ExitCode: 1, Stderr: "Traceback (most recent call last):\nAssertionError"}
	goodPass := UnittestRunObs{ExitCode: 0, Passed: true}
	goodRatios := map[[3]int64]string{{1, 5, 2}: "0", {5, 5, 2}: "1", {3, 5, 2}: "3/10"}
	contractcheck.FailErr(t, "grade project math and tests", GradeProjectMathAndTests(ProjectMathObs{
		Ratios: goodRatios, InvalidInputRejected: true, FailRun: goodFail, PassRun: goodPass,
	}))
	if err := GradeProjectMathAndTests(ProjectMathObs{
		Ratios: map[[3]int64]string{}, InvalidInputRejected: true, FailRun: goodFail, PassRun: goodPass,
	}); err == nil {
		t.Fatal("expected empty ratios map to fail")
	}
	if err := GradeProjectMathAndTests(ProjectMathObs{
		Ratios: map[[3]int64]string{{1, 5, 2}: "0", {5, 5, 2}: "1"}, InvalidInputRejected: true, FailRun: goodFail, PassRun: goodPass,
	}); err == nil {
		t.Fatal("expected missing pass_k(3,5,2)==3/10 to fail")
	}
	if err := GradeProjectMathAndTests(ProjectMathObs{
		Ratios: goodRatios, InvalidInputRejected: false, FailRun: goodFail, PassRun: goodPass,
	}); err == nil {
		t.Fatal("expected missing invalid-input rejection to fail")
	}

	// 4. Split preservation + negative controls (empty valid output, missing vs empty file, unrejected invalid).
	origSplits := map[string][]byte{"train.jsonl": []byte("{\"id\":1}\n"), "empty.jsonl": []byte{}}
	validOut := map[string][]byte{"train.jsonl": []byte("{\"id\":3}\n"), "val.jsonl": []byte("{\"id\":4}\n")}
	goodSplit := SplitPreservationObs{
		Original: origSplits, AfterInvalid: origSplits, InvalidRejected: true,
		AfterOverlap: origSplits, OverlapRejected: true,
		ExpectedValid: validOut, ValidRun1: validOut, ValidRun2: validOut,
	}
	contractcheck.FailErr(t, "grade split preservation", GradeSplitPreservation(goodSplit))

	emptyValid := goodSplit
	emptyValid.ExpectedValid = map[string][]byte{}
	emptyValid.ValidRun1 = map[string][]byte{}
	emptyValid.ValidRun2 = map[string][]byte{}
	if err := GradeSplitPreservation(emptyValid); err == nil {
		t.Fatal("expected empty valid output map to fail")
	}

	missingEmptyFile := goodSplit
	missingEmptyFile.AfterInvalid = map[string][]byte{"train.jsonl": []byte("{\"id\":1}\n")}
	if err := GradeSplitPreservation(missingEmptyFile); err == nil {
		t.Fatal("expected missing file (where original was empty) to fail")
	}

	unrejected := goodSplit
	unrejected.InvalidRejected = false
	if err := GradeSplitPreservation(unrejected); err == nil {
		t.Fatal("expected unrejected invalid JSONL to fail")
	}

	stop := "stop"
	contractcheck.FailErr(t, "grade usage ledger", GradeUsageLedger([]UsageLedgerEntry{
		{CaseKind: "absent_opt_in", RequestID: "r1", Interpreter: "portable_python"},
		{CaseKind: "empty_choices", RequestID: "r2", FinishReason: nil, Interpreter: "portable_python"},
		{CaseKind: "ordinary", RequestID: "r3", PromptTokens: 42, FinishReason: &stop, Interpreter: "project_runner"},
	}))
}
