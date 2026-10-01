package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

const (
	defaultStatsMaxFiles = 50
	maxStatsMaxFiles     = 200
	maxStatsFileBytes    = 256 * 1024
)

// StatsOptions control a stats-mode repo_map call.
type StatsOptions struct {
	Root         string
	Subpath      string
	MaxFiles     int
	PathIncluded func(relSlash string, isDir bool) bool
}

// StatsFileEntry is one text file in a stats snapshot.
type StatsFileEntry struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Lines  int    `json:"lines,omitempty"`
	IsText bool   `json:"is_text"`
}

// StatsSnapshot is returned when repo_map mode=stats.
type StatsSnapshot struct {
	Mode             string           `json:"mode"`
	Path             string           `json:"path"`
	Files            int              `json:"files"`
	Dirs             int              `json:"dirs"`
	TotalBytes       int64            `json:"total_bytes"`
	TextLines        int              `json:"text_lines,omitempty"`
	LargestText      []StatsFileEntry `json:"largest_text,omitempty"`
	OrientationBrief string           `json:"orientation_brief,omitempty"`
}

// BuildStats aggregates byte and line counts for a file or directory subtree.
func BuildStats(ctx context.Context, opts StatsOptions) (*StatsSnapshot, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, fmt.Errorf("repomap: root required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("repomap: abs root: %w", err)
	}
	sub := strings.TrimSpace(opts.Subpath)
	walkRoot := absRoot
	relPath := "."
	if sub != "" {
		relPath = filepath.ToSlash(sub)
		walkRoot = filepath.Join(absRoot, filepath.FromSlash(sub))
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultStatsMaxFiles
	}
	if maxFiles > maxStatsMaxFiles {
		maxFiles = maxStatsMaxFiles
	}
	info, err := os.Stat(walkRoot)
	if err != nil {
		return nil, err
	}
	snap := &StatsSnapshot{Mode: "stats", Path: relPath}
	if !info.IsDir() {
		entry, err := statsForFile(walkRoot, relPath)
		if err != nil {
			return nil, err
		}
		snap.Files = 1
		snap.TotalBytes = entry.Bytes
		if entry.IsText {
			snap.TextLines = entry.Lines
			snap.LargestText = []StatsFileEntry{entry}
		}
		return snap, nil
	}
	var textFiles []StatsFileEntry
	repoRel := func(abs string) string {
		rel, err := filepath.Rel(absRoot, abs)
		if err != nil {
			return filepath.ToSlash(abs)
		}
		return filepath.ToSlash(rel)
	}
	admit := func(_, abs string, isDir bool) bool {
		return opts.PathIncluded == nil || opts.PathIncluded(repoRel(abs), isDir)
	}
	walkErr := sandbox.SurveyWalk(ctx, walkRoot, sandbox.SurveyOptions{IncludeHidden: true, Admit: admit},
		func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if e.IsDir {
				snap.Dirs++
				return sandbox.SurveyContinue, nil
			}
			fi, err := e.DirEntry.Info()
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			snap.Files++
			snap.TotalBytes += fi.Size()
			entry, err := statsForFile(e.Abs, repoRel(e.Abs))
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			if entry.IsText {
				snap.TextLines += entry.Lines
				textFiles = append(textFiles, entry)
			}
			return sandbox.SurveyContinue, nil
		})
	if walkErr != nil {
		return nil, fmt.Errorf("repomap stats walk: %w", walkErr)
	}
	sort.Slice(textFiles, func(i, j int) bool {
		if textFiles[i].Bytes != textFiles[j].Bytes {
			return textFiles[i].Bytes > textFiles[j].Bytes
		}
		return textFiles[i].Path < textFiles[j].Path
	})
	if len(textFiles) > maxFiles {
		textFiles = textFiles[:maxFiles]
	}
	snap.LargestText = textFiles
	return snap, nil
}

func statsForFile(fullPath, relPath string) (StatsFileEntry, error) {
	info, err := os.Stat(fullPath)
	if err != nil {
		return StatsFileEntry{}, err
	}
	entry := StatsFileEntry{
		Path:  filepath.ToSlash(relPath),
		Bytes: info.Size(),
	}
	if info.Size() > maxStatsFileBytes {
		return entry, nil
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return StatsFileEntry{}, err
	}
	doc, _, err := textfile.Open(data, textfile.LimitsForRaw(maxStatsFileBytes))
	if err != nil {
		// Non-text file — keep the size entry without a line count.
		return entry, nil //nolint:nilerr // skip non-text
	}
	entry.IsText = true
	entry.Lines = toolkit.CountLines(doc.Text())
	return entry, nil
}
