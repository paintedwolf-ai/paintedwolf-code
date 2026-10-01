package structrewrite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/textfile"
)

const defaultWalkMaxMatches = 1000

// WalkSearch searches supported source files in path order.
// Parser failures return an error rather than an empty match set.
func WalkSearch(ctx context.Context, req WalkRequest) (files []FileMatches, truncated bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			files, truncated, err = nil, false, sourceAnalysisRecovered("walk search", req.LangName, 0, r)
		}
	}()
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return nil, false, fmt.Errorf("pattern required")
	}
	if strings.TrimSpace(req.LangName) != "" {
		if _, ok := SupportedLanguage(req.LangName, ""); !ok {
			return nil, false, &ErrLanguageUnknown{Name: req.LangName}
		}
	}
	maxFiles := req.MaxFiles
	if maxFiles <= 0 {
		maxFiles = repomap.DefaultWalkMaxFiles
	}
	maxMatches := req.MaxMatches
	if maxMatches <= 0 {
		maxMatches = defaultWalkMaxMatches
	}

	var (
		out          []FileMatches
		totalMatches int
		matchTrunc   bool
		searchErr    error
	)

	stats, walkErr := repomap.WalkSourceFiles(ctx, repomap.SourceWalkOptions{
		Root:         req.Root,
		Subpaths:     req.Subpaths,
		Recursive:    req.Recursive,
		MaxFiles:     maxFiles,
		MaxFileBytes: repomap.DefaultWalkMaxFileBytes,
		PathIncluded: req.PathIncluded,
	}, func(relSlash, absPath string) error {
		if matchTrunc {
			return repomap.ErrWalkStop
		}
		fileOut, perr := searchFile(ctx, req, relSlash, absPath, maxMatches-totalMatches)
		if perr != nil {
			searchErr = perr
			return repomap.ErrWalkStop
		}
		if len(fileOut.Matches) == 0 {
			return nil
		}
		out = append(out, fileOut)
		totalMatches += len(fileOut.Matches)
		if totalMatches >= maxMatches {
			matchTrunc = true
			return repomap.ErrWalkStop
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, repomap.ErrWalkStop) {
		return nil, false, walkErr
	}
	if searchErr != nil {
		return nil, false, searchErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	truncated = matchTrunc || stats.FilesCapHit
	return out, truncated, nil
}

func searchFile(ctx context.Context, req WalkRequest, relSlash, absPath string, room int) (out FileMatches, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = FileMatches{}, sourceAnalysisRecovered("file search for "+relSlash, req.LangName, 0, r)
		}
	}()
	if room <= 0 {
		return FileMatches{}, nil
	}
	if _, ok := SupportedLanguage(req.LangName, relSlash); !ok {
		return FileMatches{}, nil
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return FileMatches{}, nil
	}
	doc, _, err := textfile.Open(content, textfile.LimitsForRaw(repomap.DefaultWalkMaxFileBytes))
	if err != nil {
		return FileMatches{}, nil
	}
	res, err := Run(ctx, Request{
		LangName: req.LangName,
		Filename: relSlash,
		Source:   []byte(doc.Text()),
		Pattern:  req.Pattern,
	})
	if err != nil {
		return FileMatches{}, err
	}
	if len(res.Matches) == 0 {
		return FileMatches{}, nil
	}
	matches := res.Matches
	if len(matches) > room {
		matches = matches[:room]
	}
	return FileMatches{Path: relSlash, Language: res.Language, Matches: matches}, nil
}
