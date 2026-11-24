package native

import (
	"context"
	"io"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// CatalogObservation addresses a file in the literal prefilter cache independently of its grant root.
type CatalogObservation struct {
	RootPath string
	Rel      string
}

func (e ExecutionRevisions) observeFile(resolved projectpaths.Resolved, catalog *CatalogObservation, info fs.FileInfo) *sourcecatalog.ContentRead {
	if e.Literals == nil || resolved.Compressed || !info.Mode().IsRegular() {
		return nil
	}
	rootPath, rel := resolved.Root.Path, resolved.ScopeRel
	if resolved.External {
		if catalog == nil || catalog.RootPath == "" || catalog.Rel == "" {
			return nil
		}
		rootPath, rel = catalog.RootPath, catalog.Rel
	}
	if rootPath == "" || rel == "" || protectedpath.IsCredentialFile(rel) || (resolved.ScopeRel != "" && protectedpath.IsCredentialFile(resolved.ScopeRel)) {
		return nil
	}
	entry := sourcecatalog.Entry{Path: rel, Name: info.Name(), Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
	return e.Literals.BeginRead(rootPath, entry, fspath.ObserveFile(info))
}

// scanFile learns during the required capture; replay verifies every block.
func (e ExecutionRevisions) scanFile(ctx context.Context, resolved projectpaths.Resolved, file *os.File, info fs.FileInfo, proofs bool) (*verifiedSource, error) {
	return e.scanFileAt(ctx, resolved, nil, file, info, proofs)
}

func (e ExecutionRevisions) scanFileAt(ctx context.Context, resolved projectpaths.Resolved, catalog *CatalogObservation, file *os.File, info fs.FileInfo, proofs bool) (*verifiedSource, error) {
	observation := e.observeFile(resolved, catalog, info)
	defer observation.Close()
	input := observation.Wrap(ctx, io.NewSectionReader(file, 0, info.Size()))
	captured, err := e.scanSource(ctx, resolved, input, proofs)
	if err != nil {
		return nil, err
	}
	if err := captured.checkFile(file, info); err != nil {
		captured.Close()
		return nil, err
	}
	captured.learned = observation.Commit()
	return captured, nil
}
