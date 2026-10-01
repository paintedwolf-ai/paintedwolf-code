package browser

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser/designkit"
	"github.com/lycaon/lycaon/internal/browserengine"
)

// StaticOrigin is the synthetic HTTP origin for project captures.
const StaticOrigin = "http://lycaon.capture/"

// fetchAnswer is how the page's fetch handler answers one paused request.
type fetchAnswer struct {
	pass     bool
	status   int
	headers  map[string]string
	body     []byte
	fail     proto.NetworkErrorReason
	delay    time.Duration
	servedBy string
	route    *int
}

// CaptureFetch answers a page's requests: route fixtures first, then bundled font
// substitutes, then project files for a project_dir page. Everything else goes out as sent.
type CaptureFetch struct {
	Root     string
	Origin   string
	routes   *routeTable
	evidence *pageEvidence
	page     *rod.Page
	cancel   context.CancelFunc
}

// AttachCaptureFetch pauses every request the page makes so the host can answer it.
//
//nolint:contextcheck // Request interception follows the page lifetime.
func AttachCaptureFetch(ctx context.Context, page *rod.Page, root string, routes *routeTable, evidence *pageEvidence) (*CaptureFetch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root = strings.TrimSpace(root)
	origin := ""
	if root != "" {
		abs, err := PrepareStaticRoot(root)
		if err != nil {
			return nil, err
		}
		root = abs
		origin = StaticOrigin
	}
	if page == nil {
		return nil, browserengine.Reject("CAPTURE_SERVE_FAILED", map[string]any{"reason": "nil_page"})
	}
	fetchCtx, cancel := context.WithCancel(page.GetContext())
	stopRequest := context.AfterFunc(ctx, cancel)
	defer stopRequest()
	timer := time.AfterFunc(RasterizeTimeout, cancel)
	defer timer.Stop()
	m := &CaptureFetch{Root: root, Origin: origin, routes: routes, evidence: evidence, page: page.Context(fetchCtx), cancel: cancel}
	enable := proto.FetchEnable{Patterns: []*proto.FetchRequestPattern{{URLPattern: "*", RequestStage: proto.FetchRequestStageRequest}}}
	if err := enable.Call(m.page); err != nil {
		cancel()
		return nil, browserengine.Reject("CAPTURE_SERVE_FAILED", map[string]any{"reason": "fetch_enable", "detail": err.Error()})
	}
	go m.page.EachEvent(func(e *proto.FetchRequestPaused) {
		go m.answer(e)
	})()
	return m, nil
}

// Close stops answering the page's requests.
func (m *CaptureFetch) Close(ctx context.Context) error {
	if m == nil || m.cancel == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageCloseTimeout)
	defer cancel()
	err := proto.FetchDisable{}.Call(m.page.Context(ctx))
	m.cancel()
	m.cancel = nil
	return err
}

func (m *CaptureFetch) answer(e *proto.FetchRequestPaused) {
	if e == nil || e.Request == nil {
		return
	}
	a := m.decide(e.Request.Method, e.Request.URL)
	if a.servedBy != "" && m.evidence != nil {
		m.evidence.noteServed(e.NetworkID, a.servedBy, a.route)
	}
	if a.delay > 0 && pause(m.page, a.delay) != nil {
		return
	}
	// A page that navigated or closed abandons its paused requests, so a failed answer is moot.
	switch {
	case a.pass:
		_ = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(m.page)
	case a.fail != "":
		_ = proto.FetchFailRequest{RequestID: e.RequestID, ErrorReason: a.fail}.Call(m.page)
	default:
		headers := make([]*proto.FetchHeaderEntry, 0, len(a.headers))
		for k, v := range a.headers {
			headers = append(headers, &proto.FetchHeaderEntry{Name: k, Value: v})
		}
		_ = proto.FetchFulfillRequest{RequestID: e.RequestID, ResponseCode: a.status, ResponseHeaders: headers, Body: a.body}.Call(m.page)
	}
}

func (m *CaptureFetch) decide(method, rawURL string) fetchAnswer {
	reqURL, err := url.Parse(rawURL)
	if err != nil || !httpURLScheme(reqURL.Scheme) {
		return fetchAnswer{pass: true}
	}
	if rule, index, ok := m.routes.match(method, reqURL); ok {
		return routeAnswer(rule, index)
	}
	if font, ok := designkit.FontSubstitute(reqURL); ok {
		if font.Blocked {
			return fetchAnswer{fail: proto.NetworkErrorReasonBlockedByClient, servedBy: networkServedByFont}
		}
		return fetchAnswer{status: font.Status, headers: font.Headers, body: font.Body, servedBy: networkServedByFont}
	}
	if m.Root == "" {
		return fetchAnswer{pass: true}
	}
	// Mounted files are served by path regardless of host.
	if full, ok := resolveStaticFile(m.Root, reqURL.EscapedPath()); ok {
		return staticFileAnswer(full)
	}
	if m.Origin != "" && SameOrigin(m.Origin, reqURL.String()) {
		return fetchAnswer{status: http.StatusNotFound, body: []byte("not found"), servedBy: networkServedByProject}
	}
	return fetchAnswer{pass: true}
}

func httpURLScheme(scheme string) bool {
	s := strings.ToLower(strings.TrimSpace(scheme))
	return s == "http" || s == "https"
}

func staticFileAnswer(full string) fetchAnswer {
	body, err := os.ReadFile(full)
	if err != nil {
		return fetchAnswer{fail: proto.NetworkErrorReasonFailed, servedBy: networkServedByProject}
	}
	ctype := mime.TypeByExtension(filepath.Ext(full))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return fetchAnswer{status: http.StatusOK, headers: map[string]string{"Content-Type": ctype}, body: body, servedBy: networkServedByProject}
}

// resolveStaticFile maps a URL path to a regular file under root.
// Directory paths resolve to index.html (pretty-URL static exports).
func resolveStaticFile(root, urlPath string) (string, bool) {
	rel := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if rel == "." || rel == "" {
		rel = "index.html"
	}
	candidates := []string{rel}
	if !strings.HasSuffix(rel, "index.html") {
		candidates = append(candidates, path.Join(rel, "index.html"))
	}
	for _, c := range candidates {
		full, ok := safeJoinUnderRoot(root, c)
		if !ok {
			continue
		}
		fi, err := os.Stat(full)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		return full, true
	}
	return "", false
}

func safeJoinUnderRoot(root, rel string) (string, bool) {
	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", false
	}
	full := filepath.Join(root, cleanRel)
	relOut, err := filepath.Rel(root, full)
	if err != nil || pathEscapesRoot(relOut) {
		return "", false
	}

	rootCanon := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		rootCanon = r
	}

	checkPath := full
	if _, err := os.Lstat(full); os.IsNotExist(err) {
		checkPath = filepath.Dir(full)
	}
	evaluated, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Lexical path is under root; missing parents → caller 404s.
			return full, true
		}
		return "", false
	}
	if checkPath != full {
		evaluated = filepath.Join(evaluated, filepath.Base(full))
	}
	relEval, err := filepath.Rel(rootCanon, evaluated)
	if err != nil || pathEscapesRoot(relEval) {
		return "", false
	}
	return evaluated, true
}

func pathEscapesRoot(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SameOrigin reports whether targetURL shares the capture origin (scheme+host+port).
func SameOrigin(baseURL, targetURL string) bool {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	target := strings.TrimSpace(targetURL)
	if base == "" || target == "" {
		return false
	}
	if strings.HasPrefix(target, "about:") || strings.HasPrefix(target, "blob:") || strings.HasPrefix(target, "data:") {
		return true
	}
	bu, err1 := url.Parse(base)
	tu, err2 := url.Parse(target)
	if err1 != nil || err2 != nil || bu.Scheme == "" || tu.Scheme == "" {
		return false
	}
	return strings.EqualFold(bu.Scheme, tu.Scheme) && strings.EqualFold(bu.Host, tu.Host)
}

// JoinStaticEntry builds the navigate URL for project_dir captures.
// entry is a site-relative path (e.g. /talks/); empty opens the mount root.
func JoinStaticEntry(entry string) (string, error) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return StaticOrigin, nil
	}
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		return "", browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "path_not_relative", "path": entry})
	}
	if strings.ContainsAny(raw, "?#") {
		return "", browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "path_query_fragment", "path": entry})
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "path_escape", "path": entry})
		}
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(raw, "/"))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", browserengine.Reject("CAPTURE_TARGET_INVALID", map[string]any{"reason": "path_escape", "path": entry})
	}
	keepSlash := strings.HasSuffix(raw, "/") && cleaned != "/"
	u, err := url.Parse(StaticOrigin)
	if err != nil {
		return "", browserengine.Reject("CAPTURE_SERVE_FAILED", map[string]any{"reason": "static_origin", "detail": err.Error()})
	}
	u.Path = cleaned
	if keepSlash && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

// PrepareStaticRoot validates a project_dir for no-listen capture.
// The returned path is symlink-resolved so safeJoinUnderRoot Rel checks
// match the kernel path (e.g. macOS /var → /private/var).
func PrepareStaticRoot(root string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", browserengine.Reject("CAPTURE_PROJECT_DIR_INVALID", map[string]any{"path": root, "reason": "abs"})
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return "", browserengine.Reject("CAPTURE_PROJECT_DIR_MISSING", map[string]any{"path": abs})
	}
	if canon, err := filepath.EvalSymlinks(abs); err == nil {
		abs = canon
	}
	return abs, nil
}
