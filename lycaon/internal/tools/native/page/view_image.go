package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	attachmentformat "github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

const ViewImageToolName = "view_image"

// ViewImageDeps provides runtime services for visual image inspection.
type ViewImageDeps struct {
	Boundary    *sandbox.Boundary
	Raster      *browser.Rasterizer
	HandleStore renderhandle.Store
	VisualStore visual.Store
	Screen      *visualscreen.Gate
	// RootSessionID maps a session to the tree root that owns its artifacts.
	RootSessionID func(ctx context.Context, sessionID string) string
}

type viewImageArgs struct {
	Path     string             `json:"path,omitempty"`
	Handle   string             `json:"handle,omitempty"`
	Scale    float64            `json:"scale,omitempty"`
	Viewport *viewImageViewport `json:"viewport,omitempty"`
}

type viewImageViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type viewImageResult struct {
	Path        string `json:"path,omitempty"`
	Handle      string `json:"handle,omitempty"`
	Revision    int    `json:"revision,omitempty"`
	Format      string `json:"format"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	Transparent bool   `json:"transparent,omitempty"`
	Text        string `json:"text,omitempty"`
	// Perception is omitted when the model sees the image as supplied.
	Perception string `json:"perception,omitempty"`
	// ScreeningGap names text the secret screen could not read.
	ScreeningGap string `json:"screening_gap,omitempty"`
}

// ViewImageHandler builds the view_image handler for inspecting images visually.
func ViewImageHandler(deps ViewImageDeps) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseViewImageArgs(args)
		if err != nil {
			return "", err
		}
		if in.Handle != "" {
			return handleViewImageByHandle(ctx, deps, tctx, in)
		}
		return handleViewImageByPath(ctx, deps, tctx, in)
	}
}

// handleViewImageByHandle re-inspects a render handle or a stored artifact.
// Both carry bytes that already passed perception when produced, so only an
// artifact recorded as not perceived is screened again.
func handleViewImageByHandle(ctx context.Context, deps ViewImageDeps, tctx tools.ToolContext, in viewImageArgs) (string, error) {
	if deps.HandleStore != nil {
		if h, ok := deps.HandleStore.Get(tctx.Identity.SessionID, in.Handle); ok && len(h.Bytes) > 0 {
			return emitHandleView(tctx, in.Handle, "", h.Bytes, "image/png", api.VisualArtifactSourceRender, h.Caption,
				h.Canvas.Width, h.Canvas.Height, h.Revision, screened{bytes: h.Bytes, mime: "image/png", perception: visualscreen.PerceptionOriginal})
		}
	}
	if deps.VisualStore == nil {
		return "", renderHandleNotFound(in.Handle)
	}
	root := tctx.Identity.SessionID
	if deps.RootSessionID != nil {
		if r := strings.TrimSpace(deps.RootSessionID(ctx, tctx.Identity.SessionID)); r != "" {
			root = r
		}
	}
	_, res := visual.ResolveRef(ctx, deps.VisualStore, root, in.Handle)
	if !res.IsPresent() {
		return "", renderHandleNotFound(in.Handle)
	}
	meta := res.Meta()
	raw := res.Bytes()
	if !visual.IsRasterMime(meta.Mime) {
		return "", &toolrejection.ToolReject{Code: "IMAGE_FORMAT_UNSUPPORTED", Data: map[string]any{
			"path": in.Handle, "handle": in.Handle, "extension": meta.Mime, "supported_formats": strings.Join(attachmentformat.WorkspaceImageExtensions(), ", "),
		}}
	}
	view := screened{bytes: raw, mime: meta.Mime, perception: visualscreen.PerceptionOriginal}
	if !meta.Perceive {
		var err error
		view, err = screenVisual(ctx, deps.Screen, ViewImageToolName, tctx, visualscreen.VisualScreenInput{Mime: meta.Mime, RawBytes: raw},
			map[string]any{"path": in.Handle, "handle": in.Handle, "format": strings.TrimPrefix(meta.Mime, "image/")})
		if err != nil {
			return "", err
		}
	}
	kind, err := visualscreen.Classify(meta.Mime, raw)
	if err != nil {
		return "", imageRejectFor(err, map[string]any{"path": in.Handle, "handle": in.Handle, "format": strings.TrimPrefix(meta.Mime, "image/")})
	}
	if kind == visualscreen.KindSVG {
		return renderSVGView(ctx, deps.Raster, tctx, raw, meta.Caption, in, view)
	}
	if _, err := visualscreen.CheckRasterBounds(raw); err != nil {
		return "", imageRejectFor(err, map[string]any{"path": in.Handle, "handle": in.Handle, "format": strings.TrimPrefix(meta.Mime, "image/")})
	}
	// A perceived artifact viewed as stored is shown again by reference.
	storedID := ""
	if meta.Perceive && view.perception == visualscreen.PerceptionOriginal {
		storedID = meta.ID
	}
	return emitHandleView(tctx, "", storedID, raw, meta.Mime, meta.Source, meta.Caption, meta.Width, meta.Height, 0, view)
}

func renderHandleNotFound(handle string) error {
	return &toolrejection.ToolReject{Code: "RENDER_HANDLE_NOT_FOUND", Data: map[string]any{"handle": handle}}
}

func emitHandleView(tctx tools.ToolContext, renderHandle, storedID string, raw []byte, mime string, source api.VisualArtifactSource,
	caption string, width, height, revision int, view screened,
) (string, error) {
	if tctx.Effects.Out == nil {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
	}
	format := strings.TrimPrefix(mime, "image/")
	if strings.Contains(mime, "svg") {
		format = "svg"
	}
	switch {
	case view.perceive() && storedID != "":
		tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:       view.mime,
			Source:     source,
			Caption:    caption,
			Perceive:   true,
			Projected:  true,
			ArtifactID: storedID,
		}
	case view.perceive():
		tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:      view.mime,
			Bytes:     append([]byte(nil), view.bytes...),
			Source:    source,
			Caption:   caption,
			Perceive:  true,
			Projected: true,
		}
	}
	payload, _ := surveyjson.Marshal(viewImageResult{
		Handle:       renderHandle,
		Revision:     revision,
		Format:       format,
		Width:        width,
		Height:       height,
		SizeBytes:    int64(len(raw)),
		Text:         runeclamp.Clamp(view.text, 4000),
		Perception:   view.resultPerception(),
		ScreeningGap: view.gap,
	})
	return string(payload), nil
}

func handleViewImageByPath(ctx context.Context, deps ViewImageDeps, tctx tools.ToolContext, in viewImageArgs) (string, error) {
	resolved, err := projectpaths.ResolveRead(ctx, deps.Boundary, tctx, in.Path)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(resolved.DisplayPath))
	declared, ok := attachmentformat.WorkspaceImageMIME(ext)
	if !ok {
		return "", &toolrejection.ToolReject{
			Code: "IMAGE_FORMAT_UNSUPPORTED",
			Data: map[string]any{
				"path":              in.Path,
				"extension":         ext,
				"supported_formats": strings.Join(attachmentformat.WorkspaceImageExtensions(), ", "),
			},
		}
	}
	rawBytes, err := readWorkspaceImage(resolved, in.Path, visual.MaxBytesForMime(declared))
	if err != nil {
		return "", err
	}
	rejectData := map[string]any{"path": in.Path, "format": strings.TrimPrefix(declared, "image/")}
	kind, err := visualscreen.Classify(declared, rawBytes)
	if err != nil {
		return "", imageRejectFor(err, rejectData)
	}
	view, err := screenVisual(ctx, deps.Screen, ViewImageToolName, tctx, visualscreen.VisualScreenInput{
		Mime: declared, RawBytes: rawBytes, SourcePath: resolved.DisplayPath,
	}, rejectData)
	if err != nil {
		return "", err
	}
	if kind == visualscreen.KindSVG {
		return renderSVGView(ctx, deps.Raster, tctx, rawBytes, resolved.DisplayPath, in, view)
	}
	return renderRasterView(tctx, rawBytes, resolved.DisplayPath, view)
}

// readWorkspaceImage reads an image of at most maxBytes, decoded when it is stored compressed.
func readWorkspaceImage(resolved projectpaths.Resolved, modelPath string, maxBytes int) ([]byte, error) {
	raw, err := resolved.ReadBounded(int64(maxBytes))
	var tooLarge *projectpaths.FileTooLargeError
	switch {
	case err == nil:
		return raw, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, &toolrejection.ToolReject{Code: "IMAGE_NOT_FOUND", Data: map[string]any{"path": modelPath}}
	case errors.Is(err, projectpaths.ErrIsDirectory):
		return nil, &toolrejection.ToolReject{Code: "IMAGE_IS_DIRECTORY", Data: map[string]any{"path": modelPath}}
	case errors.As(err, &tooLarge):
		return nil, imageBytesExceeded(modelPath, tooLarge.Size, maxBytes)
	default:
		return nil, fmt.Errorf("read %s: %w", modelPath, err)
	}
}

func imageBytesExceeded(modelPath string, size int64, maxBytes int) error {
	return &toolrejection.ToolReject{Code: "IMAGE_BYTES_EXCEEDED", Data: map[string]any{
		"path": modelPath, "bytes": size, "max_bytes": maxBytes,
	}}
}

func parseViewImageArgs(args map[string]any) (viewImageArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return viewImageArgs{}, err
	}
	var in viewImageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return viewImageArgs{}, err
	}
	in.Path = strings.TrimSpace(in.Path)
	in.Handle = strings.TrimSpace(in.Handle)
	if (in.Path == "") == (in.Handle == "") {
		return viewImageArgs{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"reason":  "source_required",
			"message": "exactly one of path or handle is required",
		})
	}
	if in.Scale < 0 {
		return viewImageArgs{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"reason": "negative_scale",
		})
	}
	return in, nil
}
