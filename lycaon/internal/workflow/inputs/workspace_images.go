package inputs

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/visual"
)

// WorkspaceImage is a project image file read under the tool read floor.
type WorkspaceImage struct {
	DisplayPath string
	Mime        string
	Bytes       []byte
}

// WorkspaceImageReader reads one workspace path ref. A nil reader means the
// caller accepts no workspace paths.
type WorkspaceImageReader func(ctx context.Context, ref string) (WorkspaceImage, error)

// workspaceImageMimes are the file types a decision card previews from the
// workspace; each is checked by its decoder before it is stored.
var workspaceImageMimes = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

func workspaceImageFormats() []string {
	return []string{"image/png", "image/jpeg", "image/webp", "image/gif", "image/svg+xml"}
}

func workspaceUnsupported(ref, mime, reason string) error {
	return askUserReject("ASK_USER_ARTIFACT_UNSUPPORTED", map[string]any{
		"ref": ref, "reason": reason, "mime": mime,
		"ask_artifact_mime": mime, "ask_artifact_formats": workspaceImageFormats(),
	})
}

// workspaceImageReader resolves a path the way read does: attached roots,
// profile read scope, the control-plane deny, and descriptor-relative opens
// that cannot follow a symlink out of its root.
func WorkspaceImageReaderForBoundary(boundary *sandbox.Boundary, tctx tools.ToolContext) WorkspaceImageReader {
	return func(ctx context.Context, ref string) (WorkspaceImage, error) {
		resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, ref)
		if err != nil {
			return WorkspaceImage{}, err
		}
		f, err := fseffect.OpenRead(resolved.EffectLocation())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return WorkspaceImage{}, askUserReject("ASK_USER_ARTIFACT_NOT_FOUND", map[string]any{"ref": ref, "reason": "not_found"})
			}
			return WorkspaceImage{}, fmt.Errorf("open %s: %w", ref, err)
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return WorkspaceImage{}, fmt.Errorf("stat %s: %w", ref, err)
		}
		if info.IsDir() {
			return WorkspaceImage{}, workspaceUnsupported(ref, "", "is_directory")
		}
		mime, ok := workspaceImageMimes[strings.ToLower(filepath.Ext(resolved.DisplayPath))]
		if !ok {
			return WorkspaceImage{}, workspaceUnsupported(ref, "", "unsupported_extension")
		}
		limit := int64(visual.MaxBytesForMime(mime))
		raw, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return WorkspaceImage{}, fmt.Errorf("read %s: %w", ref, err)
		}
		if int64(len(raw)) > limit {
			return WorkspaceImage{}, workspaceUnsupported(ref, mime, "file_too_large")
		}
		if err := decodeWorkspaceImage(mime, raw); err != nil {
			return WorkspaceImage{}, workspaceUnsupported(ref, mime, "undecodable")
		}
		return WorkspaceImage{DisplayPath: resolved.DisplayPath, Mime: mime, Bytes: raw}, nil
	}
}

// decodeWorkspaceImage proves the bytes are the type their extension claims.
func decodeWorkspaceImage(mime string, raw []byte) error {
	if mime == "image/svg+xml" {
		return decodeSVGDocument(raw)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if "image/"+format != mime {
		return fmt.Errorf("content is %s, not %s", format, mime)
	}
	return nil
}

// decodeSVGDocument requires well-formed XML whose root element is svg.
func decodeSVGDocument(raw []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	root := ""
	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if start, ok := tok.(xml.StartElement); ok && root == "" {
			root = start.Name.Local
		}
	}
	if root != "svg" {
		return fmt.Errorf("root element is %q, not svg", root)
	}
	return nil
}
