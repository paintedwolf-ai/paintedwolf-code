package search

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// scanCodePaths answers the metadata arm before content indexing or file reads.
func scanCodePaths(ctx context.Context, files []codeFile, spec codeScanSpec) codeScanResult {
	result := codeScanResult{}
	if !spec.wantFiles || spec.fileCap <= 0 {
		return result
	}
	pathSpec := spec
	pathSpec.wantLines = false
	for _, file := range files {
		if ctx.Err() != nil {
			return result
		}
		outcome := scanOneCodeFile(ctx, codeScanJob{file: file}, 0, pathSpec)
		if outcome.fileHit == nil {
			continue
		}
		result.hits = append(result.hits, *outcome.fileHit)
		if len(result.hits) >= spec.fileCap {
			result.partial = true
			break
		}
	}
	return result
}

func visitCodePaths(ctx context.Context, gen codeGeneration, filter pathGlobFilter, prefilter codePrefilter, visit func([]codeFile) bool) error {
	after := ""
	for {
		var paths []string
		var err error
		if prefilter.active() && prefilter.fold != prefilterFoldSimple {
			paths, err = gen.reader.LiteralFilePathsPage(ctx, filter.codeScope(), prefilter.literalStrings(), prefilter.fold == prefilterFoldNone, after, sourcecatalog.TreeFilePageLimit)
		} else {
			paths, err = gen.reader.FilePathsPage(ctx, filter.codeScope(), after, sourcecatalog.TreeFilePageLimit)
		}
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return nil
		}
		after = paths[len(paths)-1]
		files := make([]codeFile, 0, len(paths))
		for _, path := range paths {
			if filter.allowsCode(path) {
				files = append(files, codeFile{root: gen.root, rootID: gen.rootID, rel: path, abs: filepath.Join(gen.rootPath, filepath.FromSlash(path))})
			}
		}
		if !visit(files) {
			return nil
		}
	}
}
