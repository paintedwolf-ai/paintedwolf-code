package sourceview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

type Origin struct {
	Path string
	Map  *SourceMap
}

// Materialize only considers paths the engine selected, preserving its ignore decisions.
// The map associates projected absolute paths with original report paths.
func Materialize(ctx context.Context, project, output string, scanned []string) (map[string]Origin, []Limitation, error) {
	project = fspath.CanonicalPath(project)
	origins := make(map[string]Origin)
	var limitations []Limitation
	for _, path := range scanned {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".html", ".htm", ".vue":
		default:
			continue
		}
		absolute := path
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(project, path)
		}
		resolved := fspath.CanonicalPath(absolute)
		rel, err := filepath.Rel(project, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, nil, fmt.Errorf("embedded target outside project: %s", path)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, nil, err
		}
		projections, partial, err := Scripts(ctx, data, strings.EqualFold(filepath.Ext(path), ".vue"))
		if err != nil {
			var limitation *Limitation
			if errors.As(err, &limitation) {
				limitation.File = path
				limitations = append(limitations, *limitation)
				continue
			}
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, limitation := range partial {
			limitation.File = path
			limitations = append(limitations, limitation)
		}
		for i, projection := range projections {
			dest := filepath.Join(output, fmt.Sprintf("%s.script-%d.%s", rel, i, projection.Extension))
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				return nil, nil, err
			}
			_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(dest), Source: bytes.NewReader(projection.Source), Mode: 0o600, DirMode: 0o750})
			if err != nil {
				return nil, nil, err
			}
			origins[dest] = Origin{Path: path, Map: projection.Map}
		}
	}
	return origins, limitations, nil
}
