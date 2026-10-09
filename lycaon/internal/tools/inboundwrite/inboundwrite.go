// Package inboundwrite stages remote bytes within the project write boundary.
package inboundwrite

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// Receipt describes one landed file by its display path.
type Receipt struct {
	Path  string
	Bytes int
}

// Write atomically replaces a destination within the tool's write scope.
// deniedCode identifies scope rejections; worker-branch rejections pass through.
func Write(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, dest string, body []byte, deniedCode string) (Receipt, error) {
	return WriteFrom(ctx, boundary, tctx, dest, bytes.NewReader(body), deniedCode)
}

// WriteFrom stages the stream on disk before review and replacement.
func WriteFrom(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, dest string, body io.Reader, deniedCode string) (Receipt, error) {
	dest = filepath.ToSlash(strings.TrimSpace(dest))
	denied := func(detail string) error {
		data := map[string]any{"path": dest}
		if detail != "" {
			data["detail"] = detail
		}
		return &toolrejection.ToolReject{Code: deniedCode, Data: data}
	}
	if dest == "" {
		return Receipt{}, denied("empty path")
	}
	if boundary == nil {
		return Receipt{}, denied("no write boundary")
	}
	if sandbox.HasParentTraversal(dest) || DestDenied(dest) {
		return Receipt{}, denied("")
	}
	resolved, err := projectpaths.ResolveWrite(ctx, boundary, tctx, dest)
	if err != nil {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) && reject.Code == "WORKER_WRITE_WITHOUT_BRANCH" {
			return Receipt{}, reject
		}
		return Receipt{}, denied("")
	}
	rel := resolved.DisplayPath
	if rel == "" {
		rel = dest
	}
	if err := beforeWorkerWrite(ctx, tctx, rel); err != nil {
		return Receipt{}, err
	}
	var captured previewCapture
	var reviewedBeforeSHA string
	counted := &countingReader{r: io.TeeReader(body, &captured)}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: resolved.EffectLocation(),
		ReviewStaged: func(target fseffect.Target, staged fseffect.Result) error {
			before, beforeSHA, err := readTargetPreview(target)
			if err != nil {
				return err
			}
			beforeText, note := before.text()
			afterText, afterNote := captured.text()
			if afterNote != "" {
				note = afterNote
			}
			if note == "" && beforeSHA != "" && beforeSHA != staged.SHA256 && beforeText == afterText {
				note = "Encoded file bytes change; decoded text is unchanged."
			}
			op := "write"
			if beforeSHA == "" {
				op = "create"
			}
			change := tools.FileChange{Path: resolved.Abs, Preview: api.ApprovalFileChange{
				Path: resolved.DisplayPath, RootID: resolved.Root.ID, Operation: op,
				Before: beforeText, After: afterText, BeforeSHA256: beforeSHA, AfterSHA256: staged.SHA256,
				BeforeBytes: before.size, AfterBytes: staged.Bytes, PreviewNote: note,
			}}
			if err := tctx.ReviewFileChanges(ctx, change); err != nil {
				return err
			}
			reviewedBeforeSHA = beforeSHA
			return nil
		},
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return verifyTargetPreview(target, reviewedBeforeSHA)
		},
		Source:  counted,
		Mode:    0o600,
		DirMode: 0o750,
	}); err != nil {
		return Receipt{}, fmt.Errorf("write dest: %w", err)
	}
	afterWorkerWrite(ctx, tctx, rel)
	return Receipt{Path: filepath.ToSlash(rel), Bytes: counted.n}, nil
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// DestDenied excludes Git metadata, environment files, and private keys.
func DestDenied(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	lower := strings.ToLower(rel)
	if lower == ".git" || strings.HasPrefix(lower, ".git/") {
		return true
	}
	base := filepath.Base(lower)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	return false
}

// Worker writes are recorded against the private branch; the investigate
// surface writes into the live tree and records nothing.
func beforeWorkerWrite(ctx context.Context, tctx tools.ToolContext, relPath string) error {
	if strings.TrimSpace(tctx.WorkerJobID) == "" || tctx.WorkerCoord == nil || tctx.TurnSurfaceID == toolcontract.SurfaceImplementInvestigate {
		return nil
	}
	return tctx.WorkerCoord.BeforeWorkerWrite(ctx, tctx, relPath)
}

func afterWorkerWrite(ctx context.Context, tctx tools.ToolContext, relPath string) {
	if strings.TrimSpace(tctx.WorkerJobID) == "" || tctx.WorkerCoord == nil || tctx.TurnSurfaceID == toolcontract.SurfaceImplementInvestigate {
		return
	}
	tctx.WorkerCoord.AfterWorkerWrite(ctx, tctx, relPath)
}
