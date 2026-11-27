package portablepython

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func loadFrozenFixtureZip(t *testing.T) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "source-fixtures.zip"))
	contractcheck.FailErr(t, "read testdata/source-fixtures.zip", err)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	contractcheck.FailErr(t, "open testdata/source-fixtures.zip", err)
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		contractcheck.FailErr(t, "open zip member "+f.Name, err)
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		contractcheck.FailErr(t, "read zip member "+f.Name, err)
		files[f.Name] = data
	}
	return files
}

func TestFrozenSourceFixturesIntegrityAndOracles(t *testing.T) {
	t.Parallel()
	files := loadFrozenFixtureZip(t)

	// 1. Verify every retained agent program SHA-256 against testdata/sources.json.
	rawSources, err := os.ReadFile(filepath.Join("testdata", "sources.json"))
	contractcheck.FailErr(t, "read testdata/sources.json", err)
	var sources map[string]struct {
		Capture string `json:"capture"`
		Source  string `json:"source"`
		SHA256  string `json:"sha256"`
	}
	contractcheck.FailErr(t, "parse testdata/sources.json", json.Unmarshal(rawSources, &sources))
	for dir, meta := range sources {
		member := dir + "/" + meta.Source
		data, ok := files[member]
		if !ok {
			t.Fatalf("missing frozen program %s in source-fixtures.zip", member)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != meta.SHA256 {
			t.Fatalf("program %s digest mismatch: want %s, got %s", member, meta.SHA256, got)
		}
	}

	// 2. Dependency review fixture: exact 8 rows (3 uv.lock + 5 requirements-cuda.txt), 0 discrepancies.
	depExp, err := ComputeDepDiff(
		files["dependency-review/before/uv.lock"],
		files["dependency-review/after/uv.lock"],
		files["dependency-review/before/train-host/requirements-cuda.txt"],
		files["dependency-review/after/train-host/requirements-cuda.txt"],
	)
	contractcheck.FailErr(t, "compute frozen dep diff", err)
	if len(depExp.Rows) != 8 {
		t.Fatalf("expected 8 frozen dependency diff rows, got %d", len(depExp.Rows))
	}
	var lockRows, reqRows int
	for _, r := range depExp.Rows {
		if r.Source == "uv.lock" {
			lockRows++
		} else if r.Source == "train-host/requirements-cuda.txt" {
			reqRows++
		}
	}
	if lockRows != 3 || reqRows != 5 {
		t.Fatalf("expected 3 uv.lock + 5 requirements rows, got %d + %d", lockRows, reqRows)
	}

	// 3. Manifest fixture: 16 repos (13 train, 3 holdout) and 5 holdout license rows.
	manifestYAML := files["workbook/config/repos.yaml"]
	var m RepoManifest
	contractcheck.FailErr(t, "parse frozen repos.yaml", yaml.Unmarshal(manifestYAML, &m))
	if len(m.Repos) != 16 {
		t.Fatalf("expected 16 repos in frozen manifest, got %d", len(m.Repos))
	}
	var licBuf bytes.Buffer
	lw := csv.NewWriter(&licBuf)
	_ = lw.Write([]string{"repository", "language", "spdx", "commit", "license_path", "expected_sha256"})
	holdoutRows := 0
	for _, r := range m.Repos {
		if r.Split != "holdout" {
			continue
		}
		for lp, digest := range r.LicenseFiles {
			_ = lw.Write([]string{r.Name, r.Language, r.SPDX, r.Commit, lp, digest})
			holdoutRows++
		}
	}
	lw.Flush()
	if holdoutRows != 5 {
		t.Fatalf("expected 5 holdout license rows, got %d", holdoutRows)
	}
	contractcheck.FailErr(t, "grade frozen holdout CSV", GradeLicenseCSV(manifestYAML, licBuf.Bytes()))

	// Build workbook matching frozen manifest and verify both frozen-replay and live modes.
	repoRows := [][]string{{"Name", "Language", "Split", "License", "Pinned Commit"}}
	for _, r := range m.Repos {
		repoRows = append(repoRows, []string{r.Name, r.Language, r.Split, r.SPDX, r.Commit})
	}
	countRows := expectedSplitCountRows(m.Repos)
	wbBytes := buildCustomXLSX(
		t,
		"Repositories", repoRows, `<pane ySplit="1" topLeftCell="A2" state="frozen"/>`,
		"Counts by Split and Language", countRows, `<pane xSplit="1" ySplit="1" topLeftCell="B2" state="frozen"/>`,
	)
	contractcheck.FailErr(t, "grade frozen workbook replay", GradeRepoWorkbook(manifestYAML, wbBytes))
	contractcheck.FailErr(t, "grade frozen workbook live", GradeRepoWorkbookWithMode(manifestYAML, wbBytes, WorkbookLiveDeliverable))

	// 4. Release anchors fixture: 6 anchors verified through GradeReleaseArchive.
	anchors := make(map[string][]byte)
	var anchorNames []string
	for name, data := range files {
		if strings.HasPrefix(name, "archive/releases/") && strings.HasSuffix(name, ".json") {
			base := path.Base(name)
			anchors[base] = data
			anchorNames = append(anchorNames, base)
		}
	}
	sort.Strings(anchorNames)
	if len(anchors) != 6 {
		t.Fatalf("expected 6 frozen release anchors, got %d", len(anchors))
	}
	archZip := buildReleaseZip(t, anchors, anchorNames, false, true, false)
	contractcheck.FailErr(t, "grade frozen 6-anchor archive", GradeReleaseArchive(anchors, archZip, archZip))

	// 5. Markdown links fixture: exact 5 local refs, docs/verify.md only existing target.
	mdFiles := make(map[string]string)
	for name, data := range files {
		if strings.HasPrefix(name, "markdown-links/") && strings.HasSuffix(name, ".md") {
			mdFiles[strings.TrimPrefix(name, "markdown-links/")] = string(data)
		}
	}
	links := ComputeMarkdownLinks(mdFiles)
	if len(links) != 5 {
		t.Fatalf("expected 5 frozen local markdown links, got %d (%+v)", len(links), links)
	}
	existingCount := 0
	for _, l := range links {
		if l.Exists {
			existingCount++
			if l.Target != "docs/verify.md" {
				t.Fatalf("unexpected existing target %q", l.Target)
			}
		}
	}
	if existingCount != 1 {
		t.Fatalf("expected exactly 1 existing target (docs/verify.md), got %d", existingCount)
	}
}
