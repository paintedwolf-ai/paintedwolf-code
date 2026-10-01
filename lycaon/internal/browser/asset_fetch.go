package browser

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser/designkit"
	"github.com/lycaon/lycaon/internal/browserengine"
)

// assetOriginHost is the synthetic host half of AssetOrigin. The hijack gate
// matches on it, so it stays the one place the host is spelled.
const assetOriginHost = "lycaon.asset"

// AssetOrigin identifies project files served from disk during rendering.
const AssetOrigin = "http://" + assetOriginHost + "/"

// RenderLoadsReference reports whether a reference in rendered markup reaches
// bytes. The offline fence fails every request except project assets under
// AssetOrigin and the kit document itself, which serves no images.
func RenderLoadsReference(ref string) bool {
	base, err := url.Parse(designkit.ViewURL())
	if err != nil {
		return true
	}
	target, err := base.Parse(strings.TrimSpace(ref))
	if err != nil {
		return false
	}
	return strings.EqualFold(target.Scheme, "http") && strings.EqualFold(target.Host, assetOriginHost)
}

// assetRasterMIMEs are raster image types served after content sniffing.
var assetRasterMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

// AssetMount serves AssetOrigin requests from the project root under a path
// jail, type allowlist, sanitizers, and byte caps. It implements the designkit
// AssetServer delegation contract; it creates no hijack router of its own.
type AssetMount struct {
	root    string
	budgets RenderBudgets

	mu         sync.Mutex
	totalBytes int
	served     map[string]bool
	order      []string
	firstErr   *browserengine.RejectError
}

// NewAssetMount validates the project root (empty root = no assets servable).
func NewAssetMount(projectRoot string, budgets RenderBudgets) (*AssetMount, error) {
	root := strings.TrimSpace(projectRoot)
	if root != "" {
		abs, err := PrepareStaticRoot(root)
		if err != nil {
			return nil, err
		}
		root = abs
	}
	return &AssetMount{root: root, budgets: budgets, served: map[string]bool{}}, nil
}

// TryHandle fulfills AssetOrigin requests; other origins return false.
func (m *AssetMount) TryHandle(h *rod.Hijack) bool {
	if m == nil || h == nil || h.Request == nil {
		return false
	}
	reqURL := h.Request.URL()
	if reqURL == nil || !strings.EqualFold(reqURL.Scheme, "http") || !strings.EqualFold(reqURL.Host, assetOriginHost) {
		return false
	}
	body, ctype, rej := m.serveAsset(reqURL.EscapedPath())
	if rej != nil {
		m.recordError(rej)
		h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
		return true
	}
	h.Response.Payload().ResponseCode = http.StatusOK
	h.Response.SetHeader("Content-Type", ctype)
	h.Response.SetBody(body)
	return true
}

// FirstError returns the first asset failure recorded during a render.
func (m *AssetMount) FirstError() *browserengine.RejectError {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firstErr
}

// Summary reports distinct served asset paths for the result kit catalog.
func (m *AssetMount) Summary() *designkit.AssetCatalogSummary {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.order) == 0 {
		return nil
	}
	n := m.budgets.MaxCatalogSamples
	if n <= 0 || n > len(m.order) {
		n = len(m.order)
	}
	return &designkit.AssetCatalogSummary{
		Count:       len(m.order),
		SamplePaths: append([]string(nil), m.order[:n]...),
	}
}

func (m *AssetMount) recordError(rej *browserengine.RejectError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.firstErr == nil {
		m.firstErr = rej
	}
}

// serveAsset resolves one root-relative URL path to allowlisted bytes.
func (m *AssetMount) serveAsset(urlPath string) ([]byte, string, *browserengine.RejectError) {
	rel, rej := normalizeAssetPath(urlPath)
	if rej != nil {
		return nil, "", rej
	}
	if m.root == "" {
		return nil, "", assetReject("RENDER_ASSET_NOT_FOUND", rel, map[string]any{"reason": "no_project_root"})
	}
	full, ok := safeJoinUnderRoot(m.root, rel)
	if !ok {
		return nil, "", assetReject("RENDER_ASSET_DENIED", rel, map[string]any{"reason": "path_escape"})
	}
	fi, err := os.Stat(full)
	if err != nil {
		reason := "stat_failed"
		if os.IsNotExist(err) {
			reason = "not_found"
		}
		return nil, "", assetReject("RENDER_ASSET_NOT_FOUND", rel, map[string]any{"reason": reason})
	}
	if !fi.Mode().IsRegular() {
		return nil, "", assetReject("RENDER_ASSET_NOT_FOUND", rel, map[string]any{"reason": "not_regular_file"})
	}
	if fi.Size() > int64(m.budgets.MaxAssetBytes) {
		return nil, "", assetReject("RENDER_ASSET_TOO_LARGE", rel, map[string]any{
			"bytes":           fi.Size(),
			"max_asset_bytes": m.budgets.MaxAssetBytes,
		})
	}
	body, err := readRenderAsset(full, m.budgets.MaxAssetBytes)
	if err != nil {
		return nil, "", assetReject("RENDER_ASSET_NOT_FOUND", rel, map[string]any{"reason": "read_failed"})
	}
	if len(body) > m.budgets.MaxAssetBytes {
		return nil, "", assetReject("RENDER_ASSET_TOO_LARGE", rel, map[string]any{"bytes": len(body), "max_asset_bytes": m.budgets.MaxAssetBytes})
	}
	ctype, rej := assetContentType(rel, body)
	if rej != nil {
		return nil, "", rej
	}
	if rej := m.account(rel, len(body)); rej != nil {
		return nil, "", rej
	}
	return body, ctype, nil
}

// account tracks per-render totals and served-path order under the total cap.
func (m *AssetMount) account(rel string, size int) *browserengine.RejectError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.totalBytes+size > m.budgets.MaxRenderBytes {
		return assetReject("RENDER_ASSET_TOO_LARGE", rel, map[string]any{
			"reason":                "render_total",
			"render_total_exceeded": true,
			"total_bytes":           m.totalBytes + size,
			"max_render_bytes":      m.budgets.MaxRenderBytes,
		})
	}
	m.totalBytes += size
	if !m.served[rel] {
		m.served[rel] = true
		m.order = append(m.order, rel)
	}
	return nil
}

// normalizeAssetPath maps a URL path to a clean root-relative slash path and
// applies the .git/ deny before any filesystem access.
func normalizeAssetPath(urlPath string) (string, *browserengine.RejectError) {
	unescaped, err := url.PathUnescape(urlPath)
	if err != nil {
		unescaped = urlPath
	}
	rel := strings.TrimPrefix(path.Clean("/"+unescaped), "/")
	if rel == "" || rel == "." {
		return "", assetReject("RENDER_ASSET_NOT_FOUND", rel, map[string]any{"reason": "empty_path"})
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".git" {
			return "", assetReject("RENDER_ASSET_DENIED", rel, map[string]any{"reason": "git_path"})
		}
	}
	return rel, nil
}

// assetContentType gates served bytes: raster images by content sniff, SVG and
// CSS by extension plus serve-time sanitize.
func assetContentType(rel string, body []byte) (string, *browserengine.RejectError) {
	switch strings.ToLower(path.Ext(rel)) {
	case ".svg":
		if rej := sanitizeSVGAsset(body); rej != nil {
			return "", assetReject(rej.Code, rel, rej.Data)
		}
		return "image/svg+xml", nil
	case ".css":
		if rej := sanitizeCSSAsset(body); rej != nil {
			return "", assetReject(rej.Code, rel, rej.Data)
		}
		return "text/css; charset=utf-8", nil
	default:
		sniffed := http.DetectContentType(body)
		base := strings.TrimSpace(strings.SplitN(sniffed, ";", 2)[0])
		if !assetRasterMIMEs[base] {
			return "", assetReject("RENDER_ASSET_TYPE", rel, map[string]any{
				"detected": base,
				"allowed":  []string{"png", "jpeg", "webp", "gif", "svg", "css"},
			})
		}
		return sniffed, nil
	}
}

func assetReject(code, rel string, data map[string]any) *browserengine.RejectError {
	merged := map[string]any{"path": rel, "render_asset_origin": AssetOrigin}
	for k, v := range data {
		merged[k] = v
	}
	return &browserengine.RejectError{Code: code, Data: merged}
}

// Read at most one byte beyond the cap, including if the file grows after stat.
func readRenderAsset(full string, maxBytes int) ([]byte, error) {
	f, err := os.Open(full) // #nosec G304 -- caller resolves the project asset path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
}
