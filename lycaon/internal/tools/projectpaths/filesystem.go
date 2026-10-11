package projectpaths

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
)

// Default filesystem authority does not attach paths to the project.
func resolveFilesystemPath(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, bool, error) {
	root, err := confine.FilesystemRootForPath(tctx.Identity.ProjectID, workspaceRoots(tctx), tctx.Files.GrantedWriteRoots, tctx.Host.SessionScratchDir, modelPath)
	if err != nil || root == "" {
		return Resolved{}, false, err
	}
	abs := fspath.CanonicalPath(modelPath)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return Resolved{}, false, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Resolved{}, false, nil
	}
	// An existing ancestor anchors creation within an approved missing root.
	anchor := existingEffectAnchor(root)
	rel, err = filepath.Rel(anchor, abs)
	if err != nil {
		return Resolved{}, false, err
	}
	if rel == "." {
		rel = ""
	}
	if b != nil {
		if op == sandbox.PathOpRead {
			err = b.AssertReadScope(ctx, anchor, rel, tctx.ProfileID())
		} else {
			err = b.AssertPathAllowed(ctx, anchor, rel, op)
		}
		if err != nil {
			return Resolved{}, false, err
		}
	}
	return Resolved{
		Abs: abs, DisplayPath: abs, ScopeRel: filepath.ToSlash(rel),
		Root: projectroot.RootRef{Path: anchor}, External: true,
	}, true, nil
}

func existingEffectAnchor(path string) string {
	for {
		_, err := os.Stat(path)
		if !os.IsNotExist(err) || filepath.Dir(path) == path {
			return path
		}
		path = filepath.Dir(path)
	}
}
