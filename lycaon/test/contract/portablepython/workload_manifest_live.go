package portablepython

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func gradeLiveRepoWorkbook(repos []RepoEntry, sheets []xlsxSheet) error {
	if len(sheets) != 2 {
		return fmt.Errorf("live workbook must have 2 sheets, got %d", len(sheets))
	}
	var repoSheet, countSheet *xlsxSheet
	for i := range sheets {
		if !sheets[i].HeaderFrozen {
			return fmt.Errorf("sheet %q header row is not frozen", sheets[i].Name)
		}
		if matchesRepoRowsUnordered(repos, sheets[i].Rows) == nil {
			repoSheet = &sheets[i]
		} else {
			countSheet = &sheets[i]
		}
	}
	if repoSheet == nil {
		return fmt.Errorf("no sheet contains all %d repository rows", len(repos))
	}
	if countSheet == nil {
		return fmt.Errorf("missing split/language counts sheet")
	}
	if err := matchWideCounts(repos, countSheet.Rows); err == nil {
		return nil
	}
	if err := matchTallCounts(repos, countSheet.Rows); err == nil {
		return nil
	}
	return fmt.Errorf("counts sheet %q matches neither valid wide nor tall split/language counts", countSheet.Name)
}

func matchesRepoRowsUnordered(repos []RepoEntry, rows [][]string) error {
	if len(rows) != len(repos)+1 {
		return fmt.Errorf("row count mismatch")
	}
	want := make(map[string]bool, len(repos))
	for _, r := range repos {
		want[strings.Join([]string{r.Name, r.Language, r.Split, r.SPDX, r.Commit}, "\x00")] = true
	}
	for _, row := range rows[1:] {
		if len(row) != 5 {
			return fmt.Errorf("expected 5 columns, got %d", len(row))
		}
		key := strings.Join(row, "\x00")
		if !want[key] {
			return fmt.Errorf("unexpected repo row %v", row)
		}
		delete(want, key)
	}
	return nil
}

func expectedPairCounts(repos []RepoEntry) map[string]int {
	counts := make(map[string]int)
	for _, r := range repos {
		counts[r.Split+"\x00"+r.Language]++
	}
	return counts
}

func matchWideCounts(repos []RepoEntry, rows [][]string) error {
	if len(rows) < 2 || len(rows[0]) < 2 {
		return fmt.Errorf("too few rows/cols for wide table")
	}
	header := rows[0]
	hasTotalCol := strings.EqualFold(header[len(header)-1], "total")
	langCols := header[1:]
	if hasTotalCol {
		langCols = header[1 : len(header)-1]
	}
	want := expectedPairCounts(repos)
	got := make(map[string]int)
	for _, row := range rows[1:] {
		if len(row) != len(header) {
			return fmt.Errorf("wide row length mismatch")
		}
		if strings.EqualFold(row[0], "total") {
			continue
		}
		split := row[0]
		rowSum := 0
		for i, lang := range langCols {
			v, err := strconv.Atoi(row[i+1])
			if err != nil {
				return err
			}
			rowSum += v
			if v > 0 {
				got[split+"\x00"+lang] = v
			}
		}
		if hasTotalCol {
			tot, err := strconv.Atoi(row[len(row)-1])
			if err != nil || tot != rowSum {
				return fmt.Errorf("wide row total mismatch for %s", split)
			}
		}
	}
	return compareCountMaps(want, got)
}

func matchTallCounts(repos []RepoEntry, rows [][]string) error {
	if len(rows) < 2 || len(rows[0]) != 3 {
		return fmt.Errorf("tall table requires 3 columns (split, language, count)")
	}
	want := expectedPairCounts(repos)
	got := make(map[string]int)
	for _, row := range rows[1:] {
		if len(row) != 3 {
			return fmt.Errorf("tall row length mismatch")
		}
		if strings.EqualFold(row[0], "total") {
			continue
		}
		v, err := strconv.Atoi(row[2])
		if err != nil {
			return err
		}
		if v > 0 {
			got[row[0]+"\x00"+row[1]] += v
		}
	}
	return compareCountMaps(want, got)
}

func compareCountMaps(want, got map[string]int) error {
	if len(want) != len(got) {
		return fmt.Errorf("count entry mismatch: want %d, got %d", len(want), len(got))
	}
	for k, v := range want {
		if got[k] != v {
			return fmt.Errorf("count mismatch for %q: want %d, got %d", k, v, got[k])
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
