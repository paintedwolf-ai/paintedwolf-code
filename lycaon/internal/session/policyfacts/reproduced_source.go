package policyfacts

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/reproducedsource"
	"github.com/lycaon/lycaon/internal/session/touchedpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

const MaxReproducedCandidateBytes = 256 * 1024

type candidateReader func(ctx context.Context, root projectroot.RootRef, relPath string, maxBytes int) ([]byte, bool, error)

var reproducedSourceFactNames = []string{
	"paintedwolf.reproduced_source_lines",
	"paintedwolf.reproduced_source_file_lines",
	"paintedwolf.reproduced_source_path",
	"paintedwolf.reproduced_source_start_line",
	"paintedwolf.reproduced_source_end_line",
}

func (m *Service) SetSourceAuthorship(authorship touchedpaths.AuthoredSource) {
	if m != nil {
		m.authorship = authorship
	}
}

// RegisterReproducedSource binds lazy providers for the five reproduced-source
// facts, sharing one measurement across all five.
func (m *Service) RegisterReproducedSource(ctx context.Context, gc *oar.GuardContext, sess *api.Session, content string) {
	if gc == nil {
		return
	}
	var (
		once sync.Once
		res  reproducedsource.Result
	)
	compute := func(target *oar.GuardContext) error {
		once.Do(func() {
			res = m.measureReproducedSource(ctx, sess, content)
		})
		target.Source.ReproducedSourceLines = int64(res.Lines)
		target.Source.ReproducedSourceFileLines = int64(res.FileLines)
		target.Source.ReproducedSourcePath = res.Path
		target.Source.ReproducedSourceStartLine = int64(res.StartLine)
		target.Source.ReproducedSourceEndLine = int64(res.EndLine)
		return nil
	}
	for _, name := range reproducedSourceFactNames {
		gc.RegisterProvider(name, compute)
	}
}

func (m *Service) measureReproducedSource(ctx context.Context, sess *api.Session, content string) reproducedsource.Result {
	if m == nil || sess == nil || content == "" {
		return reproducedsource.Result{}
	}
	doc := citationmd.Parse(content, citationmd.Options{})
	if !reproducedsource.HasMeasurableFence(doc) {
		return reproducedsource.Result{}
	}
	if m.workspace == nil {
		return reproducedsource.Result{}
	}
	roots, err := m.workspace.Roots(ctx, sess)
	if err != nil || len(roots) == 0 {
		return reproducedsource.Result{}
	}
	touched := touchedpaths.List(ctx, sess.ProjectID, sess.ID, sess.WorkspaceRootID, roots, m.store, m.authorship, touchedpaths.MaxCandidates)
	if len(touched) == 0 {
		return reproducedsource.Result{}
	}

	rootsByID := make(map[string]projectroot.RootRef, len(roots))
	for _, r := range roots {
		rootsByID[r.ID] = r
	}
	reader := m.candidateReader
	if reader == nil {
		reader = readConfinedCandidate
	}

	candidates := make([]reproducedsource.Candidate, 0, len(touched))
	for _, tp := range touched {
		root, ok := rootsByID[tp.RootID]
		if !ok || root.External || strings.TrimSpace(root.Path) == "" {
			continue
		}
		raw, ok, err := reader(ctx, root, tp.RelPath, MaxReproducedCandidateBytes)
		if err != nil || !ok || len(raw) == 0 {
			continue
		}
		if len(raw) > MaxReproducedCandidateBytes {
			raw = raw[:MaxReproducedCandidateBytes]
		}
		raw = trimIncompleteUTF8(raw)
		if len(raw) == 0 {
			continue
		}
		candidates = append(candidates, reproducedsource.Candidate{
			DisplayPath: projectroot.DisplayPath(roots, sess.WorkspaceRootID, tp.RootID, tp.RelPath),
			Content:     string(raw),
		})
	}
	if len(candidates) == 0 {
		return reproducedsource.Result{}
	}
	return reproducedsource.Measure(doc, candidates)
}

func readConfinedCandidate(_ context.Context, root projectroot.RootRef, relPath string, maxBytes int) ([]byte, bool, error) {
	f, err := fseffect.OpenRead(fseffect.Location{
		Root: root.Path,
		Rel:  filepath.FromSlash(relPath),
	})
	if err != nil {
		return nil, false, nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)))
	if err != nil {
		return nil, false, nil
	}
	return raw, true, nil
}

func trimIncompleteUTF8(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size != 1 {
			return b
		}
		// Only trim trailing continuation/start bytes of a split multi-byte sequence.
		last := b[len(b)-1]
		if last < 0x80 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}
