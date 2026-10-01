package repomap

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textrank"
	"github.com/lycaon/lycaon/internal/tsparse"
)

type buildWalkState struct {
	ctx        context.Context
	opts       Options
	absRoot    string
	walkRoot   string
	langFilter map[string]bool

	candidates []candidate
	scanned    int
}

// repoRel returns abs relative to the project root, slash-separated, matching
// the path form repo_map reports and the caller's PathIncluded predicate expect.
func (s *buildWalkState) repoRel(abs string) string {
	rel, err := filepath.Rel(s.absRoot, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// structuralScan records regular files without reading their contents.
func (s *buildWalkState) structuralScan() error {
	admit := func(_, abs string, isDir bool) bool {
		return s.opts.PathIncluded == nil || s.opts.PathIncluded(s.repoRel(abs), isDir)
	}
	return sandbox.SurveyWalk(s.ctx, s.walkRoot, sandbox.SurveyOptions{
		IncludeHidden: true, Admit: admit,
		PruneNestedVCS: s.opts.PruneNestedVCS, OnNestedRepoPruned: s.opts.OnNestedRepoPruned,
	},
		func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if e.IsDir {
				return sandbox.SurveyContinue, nil
			}
			s.scanned++
			rel := s.repoRel(e.Abs)
			var size int64
			if info, err := e.DirEntry.Info(); err == nil {
				size = info.Size()
			}
			entry := filekind.GrammarForPath(e.DirEntry.Name())
			if entry == nil {
				if len(s.langFilter) == 0 {
					s.candidates = append(s.candidates, candidate{rel: rel, size: size})
				}
				return sandbox.SurveyContinue, nil
			}
			if len(s.langFilter) > 0 && !s.langFilter[strings.ToLower(entry.Name)] {
				return sandbox.SurveyContinue, nil
			}
			s.candidates = append(s.candidates, candidate{rel: rel, lang: entry.Name, size: size})
			return sandbox.SurveyContinue, nil
		})
}

// parseCandidates reads and tags admitted candidates.
func parseCandidates(ctx context.Context, absRoot string, cands []candidate, langFilter map[string]bool) ([]scannedFile, SkipStats, []tsparse.FileFailure, error) {
	var failures []tsparse.FileFailure
	var skip SkipStats
	if len(cands) == 0 {
		return nil, skip, failures, nil
	}
	fsRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, skip, failures, err
	}
	defer func() { _ = fsRoot.Close() }()

	cache := &taggerCache{}
	files := make([]scannedFile, 0, len(cands))
	for _, c := range cands {
		if ctx.Err() != nil {
			return nil, skip, failures, ctx.Err()
		}
		if c.size > DefaultWalkMaxFileBytes {
			skip.Oversized++
			continue
		}
		abs := filepath.Join(absRoot, filepath.FromSlash(c.rel))
		src, err := readFileUnderRoot(fsRoot, absRoot, abs)
		if err != nil {
			skip.Unreadable++
			continue
		}
		entry := detectGrammar(ctx, filepath.Base(c.rel), src, filekind.DepthDeep)
		if entry == nil {
			skip.NoGrammar++
			continue
		}
		if len(langFilter) > 0 && !langFilter[strings.ToLower(entry.Name)] {
			skip.LangFiltered++
			continue
		}
		outcome := tagSourceWithGrammar(ctx, c.rel, src, *entry, cache)
		if outcome.Failure != nil && len(failures) < tsparse.MaxFailureExamples {
			failures = append(failures, tsparse.FileFailure{Path: c.rel, Phase: "source", Failure: outcome.Failure})
		}
		if outcome.Skip.ParseFailed > 0 {
			skip.ParseFailed++
			continue
		}
		if outcome.Skip.ParseIncomplete > 0 {
			skip.ParseIncomplete++
			continue
		}
		if outcome.Skip.NoTagger > 0 {
			skip.NoTagger++
			continue
		}
		if outcome.Skip.NoTags > 0 {
			skip.NoTags++
			continue
		}
		files = append(files, scannedFile{rel: c.rel, lang: entry.Name, tags: outcome.Tags})
	}
	return files, skip, failures, nil
}

func candidateLangs(cands []candidate) []string {
	set := map[string]bool{}
	for _, c := range cands {
		if c.lang != "" {
			set[c.lang] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func tagBytes(tag Tag) int {
	return len(tag.Kind) + len(tag.Name) + len(tag.File) + len(tag.Language) + 24
}

func sortTags(tags []Tag) {
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].File != tags[j].File {
			return tags[i].File < tags[j].File
		}
		return tags[i].Line < tags[j].Line
	})
}

// orderTagsForTask reorders one packed tags page in place by task relevance:
// BM25F over file and symbol, with the engine blended in on the repomap_tags
// site. Without a task the page keeps its file/line order.
func orderTagsForTask(ctx context.Context, opts Options, tags []Tag) {
	task := strings.TrimSpace(opts.Task)
	if task == "" || len(tags) < 2 {
		return
	}
	docs := make([][]textrank.Field, len(tags))
	for i, t := range tags {
		docs[i] = []textrank.Field{{Text: t.File, Weight: 3}, {Text: t.Name + " " + t.Kind, Weight: 2}, {Text: t.Signature, Weight: 1}, {Text: t.Doc, Weight: 1}}
	}
	scores := textrank.FieldScores(task, docs, textrank.CodeOptions())
	if opts.Rerank.Active(decide.SiteRepomapTags) {
		texts := make([]string, len(tags))
		for i, t := range tags {
			texts[i] = TagText(t)
		}
		scores, _ = opts.Rerank.Rerank(ctx, decide.SiteRepomapTags, task, scores, texts)
	}
	ordered := make([]Tag, len(tags))
	for i, idx := range textrank.StableOrderByScore(scores) {
		ordered[i] = tags[idx]
	}
	copy(tags, ordered)
}

// TagText is what the engine reads for one definition: where it is, what it
// is, and its signature line.
func TagText(t Tag) string {
	text := "File: " + t.File
	if t.Language != "" {
		text += " (" + t.Language + ")"
	}
	text += "\nSymbol: " + t.Name + " (" + t.Kind + ")"
	if t.Signature != "" {
		text += "\n" + t.Signature
	}
	if t.Doc != "" {
		text += "\nDoc: " + t.Doc
	}
	return text
}

