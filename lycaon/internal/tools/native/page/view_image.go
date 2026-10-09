package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

const ViewImageToolName = "view_image"

var supportedImageExts = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

func supportedExtensionsList() []string {
	return []string{".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif"}
}

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

// screened is what one visual screen released for perception.
type screened struct {
	text       string
	bytes      []byte
	mime       string
	perception visualscreen.Perception
	gap        string
}

func (s screened) perceive() bool { return s.perception != visualscreen.PerceptionWithheld }

// resultPerception is empty for the original-perception default.
func (s screened) resultPerception() string {
	if s.perception == visualscreen.PerceptionOriginal {
		return ""
	}
	return string(s.perception)
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

// screenVisual runs the secret screen for tool, mapping bound and decode failures to
// the image rejections a path read uses.
func screenVisual(ctx context.Context, gate *visualscreen.Gate, tool string, tctx tools.ToolContext, in visualscreen.VisualScreenInput, rejectData map[string]any) (screened, error) {
	out := screened{bytes: in.RawBytes, mime: in.Mime, perception: visualscreen.PerceptionOriginal}
	if gate == nil {
		kind, err := visualscreen.Classify(in.Mime, in.RawBytes)
		if err == nil {
			switch kind {
			case visualscreen.KindRaster:
				if _, err := visualscreen.CheckRasterBounds(in.RawBytes); err != nil {
					return out, imageRejectFor(err, rejectData)
				}
			case visualscreen.KindSVG:
				if scanned, err := visualscreen.NewScanner(nil).Scan(ctx, in.Mime, in.RawBytes); err == nil {
					out.text = scanned.ExtractedText
				}
			}
		}
		return out, nil
	}
	in.SessionID, in.ProjectID = tctx.Identity.SessionID, tctx.Identity.ProjectID
	in.ToolName, in.ToolCallID = tool, tctx.Identity.ToolCallID
	outcome, err := gate.Screen(ctx, in)
	if err != nil {
		if errors.Is(err, visualscreen.ErrVisualSecretWithheld) {
			return out, &toolrejection.ToolReject{Code: "IMAGE_SECRET_WITHHELD", Data: rejectData}
		}
		return out, imageRejectFor(err, rejectData)
	}
	out.text = outcome.ExtractedText
	out.bytes = outcome.PerceiveBytes
	out.mime = outcome.Mime
	out.perception = outcome.Perception
	out.gap = string(outcome.Gap)
	return out, nil
}

func imageRejectFor(err error, data map[string]any) error {
	var dims *visualscreen.DimensionsError
	if errors.As(err, &dims) {
		return &toolrejection.ToolReject{Code: "IMAGE_DIMENSIONS_EXCEEDED", Data: withImageFacts(data, map[string]any{
			"width": dims.Width, "height": dims.Height, "max_dimension": dims.Max,
		})}
	}
	if errors.Is(err, visualscreen.ErrImageUndecodable) {
		return &toolrejection.ToolReject{Code: "IMAGE_CORRUPTED", Data: withImageFacts(data, map[string]any{
			"rejection_reason": err.Error(),
		})}
	}
	return err
}

func withImageFacts(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
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
			"path": in.Handle, "handle": in.Handle, "extension": meta.Mime, "supported_formats": strings.Join(supportedExtensionsList(), ", "),
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
	declared, ok := supportedImageExts[ext]
	if !ok {
		return "", &toolrejection.ToolReject{
			Code: "IMAGE_FORMAT_UNSUPPORTED",
			Data: map[string]any{
				"path":              in.Path,
				"extension":         ext,
				"supported_formats": strings.Join(supportedExtensionsList(), ", "),
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

// renderSVGView rasterizes the screened markup; redaction already rewrote it.
func renderSVGView(
	ctx context.Context,
	raster *browser.Rasterizer,
	tctx tools.ToolContext,
	svgBytes []byte,
	displayPath string,
	in viewImageArgs,
	view screened,
) (string, error) {
	if raster == nil {
		return "", errors.New("browser rasterizer unavailable for svg rendering")
	}
	scale := in.Scale
	if scale <= 0 {
		scale = 1.0
	}
	if scale < 0.1 || scale > 4.0 {
		return "", toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{
			"reason": "invalid_scale",
			"scale":  scale,
			"min":    0.1,
			"max":    4.0,
		})
	}
	res := viewImageResult{
		Path:         displayPath,
		Format:       "svg",
		SizeBytes:    int64(len(svgBytes)),
		Transparent:  true,
		Text:         runeclamp.Clamp(view.text, 4000),
		Perception:   view.resultPerception(),
		ScreeningGap: view.gap,
	}
	if !view.perceive() {
		raw, _ := surveyjson.Marshal(res)
		return string(raw), nil
	}
	width, height := 0, 0
	if in.Viewport != nil {
		width = in.Viewport.Width
		height = in.Viewport.Height
	}
	out, err := raster.Rasterize(ctx, browser.RasterizeRequest{
		Markup:       string(view.bytes),
		Mime:         "svg",
		Width:        width,
		Height:       height,
		Scale:        scale,
		Theme:        "transparent",
		ProjectRoot:  tctx.ActiveRootPath(),
		CaptureScope: captureScope(tctx),
	})
	if err != nil {
		rej := &browserengine.RejectError{}
		if errors.As(err, &rej) {
			return "", &toolrejection.ToolReject{Code: rej.Code, Data: rej.Data}
		}
		return "", err
	}
	if tctx.Effects.Out == nil {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
	}
	caption, err := raster.ProjectCaption(ctx, captureScope(tctx), displayPath)
	if err != nil {
		caption = displayPath
	}
	tctx.Effects.Out.Visual = &tools.VisualCapture{
		Mime:      out.Mime,
		Bytes:     append([]byte(nil), out.Bytes...),
		Source:    api.VisualArtifactSourceRender,
		Caption:   caption,
		Perceive:  true,
		Projected: true,
	}
	res.Width, res.Height = out.Canvas.Width, out.Canvas.Height
	raw, _ := surveyjson.Marshal(res)
	return string(raw), nil
}

// renderRasterView reports the source dimensions and attaches the screened
// pixels normalized for the model.
func renderRasterView(tctx tools.ToolContext, rawBytes []byte, displayPath string, view screened) (string, error) {
	header, err := visualscreen.CheckRasterBounds(rawBytes)
	rejectData := map[string]any{"path": displayPath, "format": header.Format}
	if err != nil {
		return "", imageRejectFor(err, rejectData)
	}
	res := viewImageResult{
		Path:         displayPath,
		Format:       header.Format,
		Width:        header.Width,
		Height:       header.Height,
		SizeBytes:    int64(len(rawBytes)),
		Text:         runeclamp.Clamp(view.text, 4000),
		Perception:   view.resultPerception(),
		ScreeningGap: view.gap,
	}
	if view.perceive() {
		norm, err := providerwire.NormalizeImageBytes(view.bytes, view.mime, visual.MaxRasterBytes())
		if err != nil {
			return "", &toolrejection.ToolReject{Code: "IMAGE_CORRUPTED", Data: withImageFacts(rejectData, map[string]any{
				"rejection_reason": err.Error(),
			})}
		}
		if tctx.Effects.Out == nil {
			tctx.Effects.Out = &tools.ToolInvocationOut{}
		}
		tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:      norm.Mime,
			Bytes:     norm.Bytes,
			Source:    api.VisualArtifactSourceWorkspace,
			Caption:   displayPath,
			Perceive:  true,
			Projected: true,
		}
	}
	raw, _ := surveyjson.Marshal(res)
	return string(raw), nil
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
