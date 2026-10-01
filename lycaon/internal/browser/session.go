package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// LaunchOptions configures a headless browser launch.
type LaunchOptions struct {
	// CacheDir holds managed binaries and ephemeral profiles.
	CacheDir string
	// Roots adds project write roots to the sandbox.
	Roots []string
	// DisableJS disables JavaScript in the launched browser.
	DisableJS bool
}

// resolvedCacheDir keeps binary resolution and confinement on one cache root.
func (o LaunchOptions) resolvedCacheDir() string {
	if dir := strings.TrimSpace(o.CacheDir); dir != "" {
		return dir
	}
	return browserengine.ManagedCacheDir()
}

// attachedWriteRoots includes the managed cache with project roots.
func (o LaunchOptions) attachedWriteRoots() []string {
	return nonEmptyRoots(append(append([]string(nil), o.Roots...), o.resolvedCacheDir())...)
}

// LaunchHeadless starts the pinned browser with an isolated profile.
// Auto-confine uses BrowserConfinement.
func LaunchHeadless(ctx context.Context, opts LaunchOptions) (*rod.Browser, func(), error) {
	if err := confine.ValidateAttachedWriteRoots(opts.attachedWriteRoots()); err != nil {
		return nil, nil, fmt.Errorf("validate browser write roots: %w", err)
	}
	cacheDir := opts.resolvedCacheDir()
	resolved, err := browserengine.EnsureBinary(ctx, browserengine.ResolveOptions{CacheDir: cacheDir})
	if err != nil {
		return nil, nil, err
	}

	profileDir, err := os.MkdirTemp(profileParent(cacheDir), "profile-*")
	if err != nil {
		return nil, nil, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{
			"reason": "profile_dir_failed",
		})
	}

	launchCtx, cancel := context.WithTimeout(ctx, RasterizeTimeout)
	defer cancel()

	l := newHeadlessLauncher(launchCtx, resolved.Path, profileDir, opts.DisableJS)

	conf, confined, err := browserBoundary(append(opts.attachedWriteRoots(), profileDir))
	if err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, nil, err
	}

	var (
		controlURL string
		kill       func()
		launchErr  error
	)
	if confined {
		confine.LogApplied("browser", "", conf)
		controlURL, kill, launchErr = launchConfined(launchCtx, l, resolved.Path, *conf)
	} else {
		// The launch context bounds discovery while the process survives the call.
		l = l.Leakless(true).Context(context.WithoutCancel(launchCtx))
		kill = l.Kill

		type launched struct {
			url string
			err error
		}
		done := make(chan launched, 1)
		go func() {
			url, err := l.Launch()
			done <- launched{url: url, err: err}
		}()
		select {
		case res := <-done:
			controlURL, launchErr = res.url, res.err
		case <-launchCtx.Done():
			l.Kill()
			launchErr = launchCtx.Err()
		}
	}
	if launchErr != nil {
		_ = os.RemoveAll(profileDir)
		reason := "launch_failed"
		detail := launchErr.Error()
		return nil, nil, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{
			"reason":   reason,
			"source":   resolved.Source,
			"detail":   detail,
			"confined": confined,
		})
	}

	browserCtx, cancelBrowser := context.WithCancel(context.WithoutCancel(ctx))
	stopConnect := context.AfterFunc(launchCtx, cancelBrowser)
	b := rod.New().Context(browserCtx).ControlURL(controlURL)
	connectErr := b.Connect()
	stopConnect()
	if connectErr != nil || browserCtx.Err() != nil {
		cancelBrowser()
		kill()
		_ = os.RemoveAll(profileDir)
		return nil, nil, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{
			"reason": "connect_failed",
			"source": resolved.Source,
		})
	}
	cleanup := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(browserCtx), pageCloseTimeout)
		defer cancel()
		_ = b.Context(closeCtx).Close()
		cancelBrowser()
		kill()
		_ = os.RemoveAll(profileDir)
	}
	return b, cleanup, nil
}

// browserBoundary resolves confinement and rejects missing enforced boundaries.
func browserBoundary(roots []string) (*confine.Confinement, bool, error) {
	conf, confined := confine.BrowserConfinement(confine.Request{Roots: roots})
	if err := confine.RequireApplied(confined); err != nil {
		return nil, false, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{
			"reason": "confinement_unavailable",
			"detail": err.Error(),
		})
	}
	return conf, confined, nil
}

func newHeadlessLauncher(ctx context.Context, browserPath, profileDir string, disableJS bool) *launcher.Launcher {
	l := launcher.New().
		Bin(browserPath).
		// Reuse the resolved executable after confinement applies.
		Set("browser-subprocess-path", browserPath).
		Headless(true).
		NoSandbox(true). // The host boundary supplies process confinement.
		UserDataDir(profileDir).
		Set("disable-extensions", "true").
		Set("disable-dev-shm-usage", "true").
		Set("no-first-run", "true").
		Set("disable-gpu", "true").
		Set("use-mock-keychain", "true").
		Set("password-store", "basic").
		Context(ctx)
	if disableJS {
		l = l.Set("disable-javascript", "true")
	}
	return l
}

// launchConfined starts a browser that outlives the discovery deadline.
func launchConfined(ctx context.Context, l *launcher.Launcher, chromePath string, c confine.Confinement) (string, func(), error) {
	// The confinement helper replaces itself with the browser.
	l = l.Delete(flags.Leakless)
	args := l.FormatArgs()

	// Keep context values while allowing the browser to outlive this call.
	cmd, profileCleanup, err := execpkg.PrepareCommand(context.WithoutCancel(ctx), chromePath, args, execpkg.ExecOpts{
		Launch: execpkg.AgentLaunch(execpkg.LaunchManagedBrowser, "chrome", &c).WithReducedEnvironment(),
	})
	if err != nil {
		return "", nil, err
	}
	parser := launcher.NewURLParser().Context(ctx)
	cmd.Stdout = parser
	cmd.Stderr = parser
	if err := cmd.Start(); err != nil {
		profileCleanup()
		return "", nil, err
	}
	// The browser stays in the engine's process group, so it is tracked alone;
	// its helpers exit with it.
	untrack := execpkg.TrackProcess(cmd.Process.Pid)

	exit := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		untrack()
		// Natural exits also close the confinement profile pipe.
		profileCleanup()
		close(exit)
	}()

	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-exit:
		case <-time.After(2 * time.Second):
		}
	}

	u, err := waitParserURL(ctx, parser, exit)
	if err != nil {
		// Err synchronizes with concurrent stdout and stderr parsing.
		detail := trimBuf(parser.Err().Error())
		kill()
		return "", nil, fmt.Errorf("%w: %s", err, detail)
	}
	resolved, err := launcher.ResolveURL(u)
	if err != nil {
		kill()
		return "", nil, err
	}
	return resolved, kill, nil
}

func waitParserURL(ctx context.Context, parser *launcher.URLParser, exit <-chan struct{}) (string, error) {
	select {
	case u := <-parser.URL:
		return u, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-exit:
		return "", fmt.Errorf("browser exited before publishing debug URL")
	}
}

func profileParent(cacheDir string) string {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return os.TempDir()
	}
	dir := filepath.Join(cacheDir, "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301 — ephemeral rod profile dir
		return os.TempDir()
	}
	return dir
}

func nonEmptyRoots(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func trimBuf(s string) string {
	const max = 2000
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

// Pool manages a reusable headless browser for a sidecar process.
type Pool struct {
	mu           sync.Mutex
	cacheDir     string
	browser      *rod.Browser
	cleanup      func()
	projector    *captureprojection.Projector
	disconnected <-chan struct{}
}

// SetCaptureProjector installs the shared safe capture projection.
func (p *Pool) SetCaptureProjector(projector *captureprojection.Projector) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.projector = projector
	p.mu.Unlock()
}

func (p *Pool) captureProjector() *captureprojection.Projector {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.projector
}

// NewPool returns a browser pool that lazy-launches on first use.
func NewPool(cacheDir string) *Pool {
	return &Pool{cacheDir: cacheDir}
}

// Browser returns a connected JS-enabled browser, launching if needed.
func (p *Pool) Browser(ctx context.Context) (*rod.Browser, error) {
	if p == nil {
		return nil, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_pool"})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.browserLocked(ctx)
}

func (p *Pool) browserLocked(ctx context.Context) (*rod.Browser, error) {
	if p.browser != nil {
		select {
		case <-p.disconnected:
			p.invalidateLocked()
		default:
			return p.browser, nil
		}
	}
	b, cleanup, err := LaunchHeadless(ctx, LaunchOptions{
		CacheDir:  p.cacheDir,
		DisableJS: false,
	})
	if err != nil {
		return nil, err
	}
	p.browser = b
	p.cleanup = cleanup
	done := make(chan struct{})
	p.disconnected = done
	events := b.Event()
	go func() {
		defer close(done)
		for range events {
		}
	}()
	return p.browser, nil
}

func (p *Pool) invalidateLocked() {
	if p.cleanup != nil {
		p.cleanup()
	}
	p.browser = nil
	p.cleanup = nil
	p.disconnected = nil
}

// Close shuts down the pooled browser.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidateLocked()
}

// NewPage opens a viewport without invalidating the pool on page errors.
func (p *Pool) NewPage(ctx context.Context, width, height int) (*rod.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := p.Browser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := createPage(ctx, b)
	if err != nil {
		return nil, cdpUnavailable("page_failed", err)
	}
	ctx, cancel := pageOperationContext(ctx, page, RasterizeTimeout)
	defer cancel()
	op := page.Context(ctx)
	if err := op.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             width,
		Height:            height,
		DeviceScaleFactor: RasterScale(width, height),
		Mobile:            false,
	}); err != nil {
		_ = closePage(ctx, page)
		return nil, cdpUnavailable("viewport_failed", err)
	}
	return page, nil
}
