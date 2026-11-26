package portablepython

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// RepoManifest mirrors config/repos.yaml for independent CSV and XLSX grading.
type RepoManifest struct {
	Repos []RepoEntry `yaml:"repos"`
}

// RepoEntry is one repository record in config/repos.yaml.
type RepoEntry struct {
	Name         string            `yaml:"name"`
	Language     string            `yaml:"language"`
	Split        string            `yaml:"split"`
	SPDX         string            `yaml:"spdx"`
	Commit       string            `yaml:"commit"`
	LicenseFiles map[string]string `yaml:"license_files"`
}

// GradeLicenseCSV independently verifies holdout_repos.csv against repos.yaml.
func GradeLicenseCSV(manifestYAML, csvBytes []byte) error {
	var m RepoManifest
	if err := yaml.Unmarshal(manifestYAML, &m); err != nil {
		return fmt.Errorf("parse repos.yaml: %w", err)
	}
	want := make(map[string]bool)
	for _, r := range m.Repos {
		if r.Split != "holdout" {
			continue
		}
		for lp, digest := range r.LicenseFiles {
			key := strings.Join([]string{r.Name, r.Language, r.SPDX, r.Commit, lp, digest}, "\x00")
			want[key] = true
		}
	}
	records, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
	if err != nil {
		return fmt.Errorf("parse holdout CSV: %w", err)
	}
	if len(records) < 2 {
		return fmt.Errorf("holdout CSV missing rows")
	}
	if len(records)-1 != len(want) {
		return fmt.Errorf("holdout row count mismatch: want %d, got %d", len(want), len(records)-1)
	}
	for _, rec := range records[1:] {
		if len(rec) != 6 {
			return fmt.Errorf("expected 6 columns, got %d", len(rec))
		}
		key := strings.Join(rec, "\x00")
		if !want[key] {
			return fmt.Errorf("unexpected holdout license row: %v", rec)
		}
		delete(want, key)
	}
	return nil
}

type xlsxSheet struct {
	Frozen bool
	Rows   [][]string
}

func readXLSXSheets(xlsxBytes []byte) (map[string]xlsxSheet, error) {
	zr, err := zip.NewReader(bytes.NewReader(xlsxBytes), int64(len(xlsxBytes)))
	if err != nil {
		return nil, fmt.Errorf("open xlsx zip: %w", err)
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	readEntry := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing xlsx entry %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	var sharedStrings []string
	if rawSS, err := readEntry("xl/sharedStrings.xml"); err == nil {
		var sst struct {
			Items []struct {
				T string `xml:"t"`
			} `xml:"si"`
		}
		if err := xml.Unmarshal(rawSS, &sst); err != nil {
			return nil, err
		}
		for _, si := range sst.Items {
			sharedStrings = append(sharedStrings, si.T)
		}
	}

	rawWB, err := readEntry("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(rawWB, &wb); err != nil {
		return nil, err
	}
	rawRels, err := readEntry("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(rawRels, &rels); err != nil {
		return nil, err
	}
	relTarget := make(map[string]string, len(rels.Items))
	for _, r := range rels.Items {
		relTarget[r.ID] = strings.TrimPrefix(r.Target, "/")
	}

	out := make(map[string]xlsxSheet, len(wb.Sheets))
	for _, sh := range wb.Sheets {
		target := relTarget[sh.ID]
		if !strings.HasPrefix(target, "xl/") {
			target = path.Join("xl", target)
		}
		rawSheet, err := readEntry(target)
		if err != nil {
			return nil, err
		}
		var ws struct {
			Views []struct {
				Pane *struct {
					State string `xml:"state,attr"`
				} `xml:"pane"`
			} `xml:"sheetViews>sheetView"`
			Rows []struct {
				Cells []struct {
					Type   string `xml:"t,attr"`
					Val    string `xml:"v"`
					Inline string `xml:"is>t"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := xml.Unmarshal(rawSheet, &ws); err != nil {
			return nil, err
		}
		frozen := len(ws.Views) > 0 && ws.Views[0].Pane != nil && ws.Views[0].Pane.State == "frozen"
		rows := make([][]string, 0, len(ws.Rows))
		for _, r := range ws.Rows {
			vals := make([]string, 0, len(r.Cells))
			for _, c := range r.Cells {
				switch c.Type {
				case "s":
					idx, _ := strconv.Atoi(c.Val)
					if idx >= 0 && idx < len(sharedStrings) {
						vals = append(vals, sharedStrings[idx])
					} else {
						vals = append(vals, c.Val)
					}
				case "inlineStr":
					vals = append(vals, c.Inline)
				default:
					vals = append(vals, c.Val)
				}
			}
			rows = append(rows, vals)
		}
		out[sh.Name] = xlsxSheet{Frozen: frozen, Rows: rows}
	}
	return out, nil
}

// GradeRepoWorkbook independently reopens repositories.xlsx and verifies all rows, counts, and frozen panes.
func GradeRepoWorkbook(manifestYAML, xlsxBytes []byte) error {
	var m RepoManifest
	if err := yaml.Unmarshal(manifestYAML, &m); err != nil {
		return fmt.Errorf("parse repos.yaml: %w", err)
	}
	sheets, err := readXLSXSheets(xlsxBytes)
	if err != nil {
		return err
	}
	repoSheet, ok := sheets["Repositories"]
	if !ok {
		return fmt.Errorf("missing Repositories sheet")
	}
	if !repoSheet.Frozen {
		return fmt.Errorf("Repositories sheet header is not frozen")
	}
	if len(repoSheet.Rows) != len(m.Repos)+1 {
		return fmt.Errorf("Repositories row count mismatch: want %d, got %d", len(m.Repos)+1, len(repoSheet.Rows))
	}
	for i, r := range m.Repos {
		want := []string{r.Name, r.Language, r.Split, r.SPDX, r.Commit}
		got := repoSheet.Rows[i+1]
		if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
			return fmt.Errorf("Repositories row %d mismatch: want %v, got %v", i+1, want, got)
		}
	}

	countSheet, ok := sheets["Counts by Split and Language"]
	if !ok {
		return fmt.Errorf("missing Counts by Split and Language sheet")
	}
	if !countSheet.Frozen {
		return fmt.Errorf("Counts by Split and Language sheet header is not frozen")
	}
	wantRows := expectedSplitCountRows(m.Repos)
	if len(countSheet.Rows) != len(wantRows) {
		return fmt.Errorf("counts sheet row mismatch: want %d, got %d", len(wantRows), len(countSheet.Rows))
	}
	for i := range wantRows {
		if strings.Join(wantRows[i], "\x00") != strings.Join(countSheet.Rows[i], "\x00") {
			return fmt.Errorf("counts sheet row %d mismatch: want %v, got %v", i, wantRows[i], countSheet.Rows[i])
		}
	}
	return nil
}

func expectedSplitCountRows(repos []RepoEntry) [][]string {
	splitSet, langSet := make(map[string]bool), make(map[string]bool)
	counts := make(map[string]int)
	for _, r := range repos {
		splitSet[r.Split] = true
		langSet[r.Language] = true
		counts[r.Split+"\x00"+r.Language]++
	}
	var splits, langs []string
	for s := range splitSet {
		splits = append(splits, s)
	}
	for l := range langSet {
		langs = append(langs, l)
	}
	sort.Strings(splits)
	sort.Strings(langs)

	wantRows := make([][]string, 0, len(splits)+2)
	wantRows = append(wantRows, append(append([]string{"Split"}, langs...), "Total"))
	for _, s := range splits {
		row := []string{s}
		sum := 0
		for _, l := range langs {
			c := counts[s+"\x00"+l]
			sum += c
			row = append(row, strconv.Itoa(c))
		}
		row = append(row, strconv.Itoa(sum))
		wantRows = append(wantRows, row)
	}
	totalRow := []string{"Total"}
	grand := 0
	for _, l := range langs {
		colSum := 0
		for _, s := range splits {
			colSum += counts[s+"\x00"+l]
		}
		grand += colSum
		totalRow = append(totalRow, strconv.Itoa(colSum))
	}
	totalRow = append(totalRow, strconv.Itoa(grand))
	return append(wantRows, totalRow)
}
