package worker

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func (p PromoteRoots) WritePrimaryBytes(task *api.WorkerTask, qualifiedPath string, content []byte) error {
	qualifiedPath = filepath.ToSlash(strings.TrimSpace(qualifiedPath))
	if qualifiedPath == "" || qualifiedPath == "." || sandbox.HasParentTraversal(qualifiedPath) {
		return nil
	}
	id, err := p.locateIdentity(task, qualifiedPath)
	if err != nil {
		return err
	}
	return p.writePrimaryBytesAt(task, id.RootID, id.ScopeRel, content, nil, nil)
}

func (p PromoteRoots) WritePrimaryText(task *api.WorkerTask, qualifiedPath, content, encoding string) error {
	raw, err := textfile.EncodeBounded(content, encoding,
		textfile.LimitsForRaw(promoteConflictMaxFileBytes))
	if err != nil {
		return err
	}
	return p.WritePrimaryBytes(task, qualifiedPath, raw)
}
