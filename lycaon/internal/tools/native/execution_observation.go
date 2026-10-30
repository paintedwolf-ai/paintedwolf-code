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

func (e ExecutionRevisions) observeFile(resolved projectpaths.Resolved, info fs.FileInfo) *sourcecatalog.ContentRead {
	if e.Literals == nil || resolved.External || resolved.Compressed || protectedpath.IsCredentialFile(resolved.ScopeRel) || !info.Mode().IsRegular() {
		return nil
	}
	entry := sourcecatalog.Entry{Path: resolved.ScopeRel, Name: info.Name(), Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
	return e.Literals.BeginRead(resolved.Root.Path, entry, fspath.ObserveFile(info))
}

// scanFile learns during the required capture; replay still verifies every block.
func (e ExecutionRevisions) scanFile(ctx context.Context, resolved projectpaths.Resolved, file *os.File, info fs.FileInfo, proofs bool) (*verifiedSource, error) {
	observation := e.observeFile(resolved, info)
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
