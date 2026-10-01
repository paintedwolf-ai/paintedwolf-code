package repoinfo

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	enry "github.com/go-enry/go-enry/v2"
	"github.com/lycaon/lycaon/internal/backgroundwork"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// Classification yields host resources between bounded batches.
const classifyBatchSize = 32

// sniffer reads a bounded head of one file for content classification.
type sniffer func(absPath string) []byte

// classifiedFile binds a classification to observed file metadata.
type classifiedFile struct {
	size     int64
	modified time.Time
	lang     string
}

// languageMemo caches classifications that required file content.
type languageMemo struct {
	mu     sync.Mutex
	byRoot map[string]map[string]classifiedFile
}

func newLanguageMemo() *languageMemo {
	return &languageMemo{byRoot: make(map[string]map[string]classifiedFile)}
}

// lookup returns the remembered classification when the file is unchanged.
func (m *languageMemo) lookup(root, rel string, size int64, modified time.Time) (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	hit, ok := m.byRoot[root][rel]
	if !ok || hit.size != size || !hit.modified.Equal(modified) {
		return "", false
	}
	return hit.lang, true
}

// replace installs the current classifications for one root.
func (m *languageMemo) replace(root string, files map[string]classifiedFile) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byRoot[root] = files
}

// forget releases every root's classifications.
func (m *languageMemo) forget() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byRoot = make(map[string]map[string]classifiedFile)
}

// sniffJob is one file whose language needs its bytes.
type sniffJob struct {
	rel  string
	size int64
	mod  time.Time
	lang string
}

// catalogAnalysis classifies one root's catalog entries into a Brief.
type catalogAnalysis struct {
	projectDir string
	memo       *languageMemo
	sniff      sniffer
}

// run reads the root's files a page at a time; the projection returns only
// regular files.
func (a catalogAnalysis) run(ctx context.Context, reader *sourcecatalog.IndexReader) (*Brief, error) {
	bytesByLang := make(map[string]int64)
	layout := newLayoutWalkState()
	remembered := make(map[string]classifiedFile)
	var pending []*sniffJob
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := reader.FilePage(ctx, sourcecatalog.FileScope{Audience: sourcecatalog.AgentAudience, IncludeHidden: true}, after, sourcecatalog.TreeFilePageLimit)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			break
		}
		after = entries[len(entries)-1].Path
		for _, entry := range entries {
			layout.noteFile(entry.Path)
			if entry.Size == 0 {
				continue
			}
			if lang, ok := languageByExtension(entry.Path); ok {
				if lang != "" {
					bytesByLang[lang] += entry.Size
				}
				continue
			}
			if lang, ok := a.memo.lookup(a.projectDir, entry.Path, entry.Size, entry.Modified); ok {
				remembered[entry.Path] = classifiedFile{size: entry.Size, modified: entry.Modified, lang: lang}
				if lang != "" {
					bytesByLang[lang] += entry.Size
				}
				continue
			}
			pending = append(pending, &sniffJob{rel: entry.Path, size: entry.Size, mod: entry.Modified})
		}
	}
	if err := a.sniffAll(ctx, pending); err != nil {
		return nil, err
	}
	for _, job := range pending {
		remembered[job.rel] = classifiedFile{size: job.size, modified: job.mod, lang: job.lang}
		if job.lang != "" {
			bytesByLang[job.lang] += job.size
		}
	}
	a.memo.replace(a.projectDir, remembered)
	return &Brief{
		Languages: dominantLanguages(bytesByLang), FileCount: layout.fileCount,
		Layout: layout.finalize(), GeneratedAt: time.Now().UTC(),
	}, nil
}

// sniffAll classifies the pending files by content, several at a time.
func (a catalogAnalysis) sniffAll(ctx context.Context, pending []*sniffJob) error {
	if len(pending) == 0 {
		return nil
	}
	for start := 0; start < len(pending); start += classifyBatchSize {
		release, err := backgroundwork.Process().Acquire(ctx, backgroundwork.Request{
			Lane: a.projectDir, Priority: backgroundwork.PriorityProactive,
			Resources: []backgroundwork.Resource{backgroundwork.ResourceCPU, backgroundwork.ResourceIO},
		})
		if err != nil {
			return err
		}
		for _, job := range pending[start:min(start+classifyBatchSize, len(pending))] {
			if err := ctx.Err(); err != nil {
				release()
				return err
			}
			job.lang = languageByContent(job.rel, a.sniff(filepath.Join(a.projectDir, filepath.FromSlash(job.rel))))
		}
		release()
	}
	return nil
}

// languageByExtension reports whether the name decides classification.
func languageByExtension(relPath string) (string, bool) {
	lang, safe := enry.GetLanguageByExtension(relPath)
	if !safe || lang == "" {
		return "", false
	}
	if isReportable(lang) {
		return lang, true
	}
	return "", true
}

// languageByContent classifies a file from a bounded head of its bytes.
func languageByContent(relPath string, content []byte) string {
	if len(content) == 0 || enry.IsBinary(content) {
		return ""
	}
	lang := enry.GetLanguage(relPath, content)
	if !isReportable(lang) {
		return ""
	}
	return lang
}

func readHead(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, MaxClassifyBytes)
	n, _ := f.Read(buf)
	return buf[:n]
}

func isReportable(lang string) bool {
	if lang == "" {
		return false
	}
	switch enry.GetLanguageType(lang) {
	case enry.Programming, enry.Markup:
		return true
	default:
		return false
	}
}
