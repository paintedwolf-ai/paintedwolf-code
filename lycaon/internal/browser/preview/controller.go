// Package preview streams a read-only CDP screencast of held/driven pages to Den.
package preview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// DefaultMaxFPS caps published frames (CDP may emit faster; we coalesce).
	DefaultMaxFPS = 4
	// DefaultJPEGQuality is CDP screencast JPEG quality (0–100).
	DefaultJPEGQuality = 45
	// DefaultMaxWidth caps screencast frame width in CSS pixels.
	DefaultMaxWidth = 960
	// DefaultMaxHeight caps screencast frame height in CSS pixels.
	DefaultMaxHeight = 720
	// MaxJPEGB64Chars drops oversized frames before publish (hub backpressure).
	MaxJPEGB64Chars = 180_000
)

// Publisher emits preview SSE envelopes for a project/session.
type Publisher func(ctx context.Context, projectID, sessionID string, ev api.PreviewEvent)

// Config tunes screencast bounds.
type Config struct {
	MaxFPS      int
	JPEGQuality int
	MaxWidth    int
	MaxHeight   int
}

// DefaultConfig returns production preview defaults.
func DefaultConfig() Config {
	return Config{
		MaxFPS:      DefaultMaxFPS,
		JPEGQuality: DefaultJPEGQuality,
		MaxWidth:    DefaultMaxWidth,
		MaxHeight:   DefaultMaxHeight,
	}
}

// AttachOpts identifies a live page for the preview stream.
type AttachOpts struct {
	ProjectID          string
	SessionID          string
	ParentSessionID    string
	PageID             string
	AssistantMessageID string
	ToolCallID         string
	Held               *browser.HeldPage
}

// Controller manages one screencast stream per page id, gated by Den watch.
type Controller struct {
	mu           sync.Mutex
	cfg          Config
	publish      Publisher
	pages        map[string]*stream // key: sessionID\0pageID
	watching     map[string]bool    // key: watchingSessionID\0pageID
	projector    *captureprojection.Projector
	projectFrame frameProjector
}

type frameProjector func(
	context.Context, *captureprojection.Projector, captureprojection.Scope,
	browser.PageRegions, string, []byte,
) ([]byte, error)

// SetCaptureProjector installs the browser owner's safe frame projection.
func (c *Controller) SetCaptureProjector(projector *captureprojection.Projector) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.projector = projector
	c.mu.Unlock()
}

type stream struct {
	projectID          string
	sessionID          string
	parentSessionID    string
	pageID             string
	assistantMessageID string
	toolCallID         string
	held               *browser.HeldPage
	minInterval        time.Duration
	cast               *castHandle
	driving            bool
	seq                int64
	lastPublish        time.Time
	lastAccept         time.Time
	pending            *pendingFrame
}

// castHandle tracks one screencast run.
type castHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type pendingFrame struct {
	data   []byte
	width  int
	height int
	url    string
	title  string
	idle   *bool
	// regions belongs to this frame, even when publication is delayed.
	regions browser.PageRegions
}

// NewController constructs an empty preview controller.
func NewController(cfg Config, publish Publisher) *Controller {
	if cfg.MaxFPS <= 0 {
		cfg.MaxFPS = DefaultMaxFPS
	}
	if cfg.JPEGQuality <= 0 {
		cfg.JPEGQuality = DefaultJPEGQuality
	}
	if cfg.MaxWidth <= 0 {
		cfg.MaxWidth = DefaultMaxWidth
	}
	if cfg.MaxHeight <= 0 {
		cfg.MaxHeight = DefaultMaxHeight
	}
	return &Controller{
		cfg: cfg, publish: publish,
		pages: make(map[string]*stream), watching: make(map[string]bool),
		projectFrame: func(
			ctx context.Context, projector *captureprojection.Projector, scope captureprojection.Scope,
			regions browser.PageRegions, mime string, raw []byte,
		) ([]byte, error) {
			safe, _, err := browser.ProjectRasterRegions(
				ctx, projector, scope, regions, mime, raw, browser.CaptureRasterGeometry{},
			)
			return safe, err
		},
	}
}

// Attach registers a held/driven page and emits an attach event.
func (c *Controller) Attach(ctx context.Context, opts AttachOpts) {
	if c == nil || opts.Held == nil || opts.Held.Page == nil {
		return
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	pageID := strings.TrimSpace(opts.PageID)
	assistantMessageID := strings.TrimSpace(opts.AssistantMessageID)
	toolCallID := strings.TrimSpace(opts.ToolCallID)
	if sessionID == "" || pageID == "" || assistantMessageID == "" || toolCallID == "" {
		return
	}
	title := pageTitle(opts.Held.Page)
	key := streamKey(sessionID, pageID)
	st := &stream{
		projectID:          strings.TrimSpace(opts.ProjectID),
		sessionID:          sessionID,
		parentSessionID:    strings.TrimSpace(opts.ParentSessionID),
		pageID:             pageID,
		assistantMessageID: assistantMessageID,
		toolCallID:         toolCallID,
		held:               opts.Held,
		minInterval:        time.Second / time.Duration(c.cfg.MaxFPS),
	}
	stopPrev := func() {}
	c.mu.Lock()
	if prev := c.pages[key]; prev != nil {
		stopPrev = c.stopCastLocked(ctx, prev)
		delete(c.pages, key)
	}
	c.pages[key] = st
	watching := c.streamWatchedLocked(st)
	projectID, eventSessionID, event := c.stampEventLocked(st, api.PreviewEvent{
		Op:              api.PreviewEventOpAttach,
		URL:             opts.Held.TargetURL,
		Title:           title,
		Width:           opts.Held.Width,
		Height:          opts.Held.Height,
		ParentSessionID: st.parentSessionID,
	})
	c.mu.Unlock()
	stopPrev()

	c.publishEvent(ctx, projectID, eventSessionID, event)
	if watching {
		c.startCast(ctx, st)
	}
}

// Claim moves a page preview to the active tool invocation.
func (c *Controller) Claim(ctx context.Context, sessionID, pageID, assistantMessageID, toolCallID string) {
	if c == nil {
		return
	}
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	toolCallID = strings.TrimSpace(toolCallID)
	if assistantMessageID == "" || toolCallID == "" {
		return
	}
	key := streamKey(strings.TrimSpace(sessionID), strings.TrimSpace(pageID))
	c.mu.Lock()
	st := c.pages[key]
	if st == nil {
		c.mu.Unlock()
		return
	}
	st.assistantMessageID = assistantMessageID
	st.toolCallID = toolCallID
	idle := !st.driving
	projectID, eventSessionID, event := c.stampEventLocked(st, api.PreviewEvent{
		Op:   api.PreviewEventOpState,
		Idle: &idle,
	})
	c.mu.Unlock()
	c.publishEvent(ctx, projectID, eventSessionID, event)
}

// Detach stops screencast and emits detach.
func (c *Controller) Detach(ctx context.Context, sessionID, pageID string) {
	if c == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	pageID = strings.TrimSpace(pageID)
	key := streamKey(sessionID, pageID)
	stop := func() {}
	c.mu.Lock()
	st := c.pages[key]
	if st != nil {
		stop = c.stopCastLocked(ctx, st)
		delete(c.pages, key)
	}
	c.mu.Unlock()
	if st == nil {
		return
	}
	stop()
	c.emit(ctx, st, api.PreviewEvent{
		Op:              api.PreviewEventOpDetach,
		ParentSessionID: st.parentSessionID,
		URL:             st.held.TargetURL,
	})
}

// SetWatching toggles one session/page preview watch.
func (c *Controller) SetWatching(ctx context.Context, sessionID string, watching bool, pageID string) api.PreviewWatchResult {
	pageID = strings.TrimSpace(pageID)
	out := api.PreviewWatchResult{PageID: pageID}
	if c == nil {
		return out
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || pageID == "" {
		return out
	}
	var starts []*stream
	var stops []func()
	c.mu.Lock()
	watchKey := streamKey(sessionID, pageID)
	if watching {
		c.watching[watchKey] = true
	} else {
		delete(c.watching, watchKey)
	}
	for _, st := range c.pages {
		if st.sessionID != sessionID && st.parentSessionID != sessionID {
			continue
		}
		if st.pageID != pageID {
			continue
		}
		if watching {
			starts = append(starts, st)
		} else if !c.streamWatchedLocked(st) {
			stops = append(stops, c.stopCastLocked(ctx, st))
		}
	}
	c.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
	for _, st := range starts {
		c.startCast(ctx, st)
	}
	out.Watching = watching
	return out
}

// SnapshotForSession returns held preview attachments for one session.
func (c *Controller) SnapshotForSession(ctx context.Context, sessionID string) []api.PreviewAttachment {
	if c == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	c.mu.Lock()
	out := make([]api.PreviewAttachment, 0)
	projectIDs := make([]string, 0)
	for _, st := range c.pages {
		if st.sessionID != sessionID && st.parentSessionID != sessionID {
			continue
		}
		idle := !st.driving
		attachment := api.PreviewAttachment{
			SessionID:          st.sessionID,
			PageID:             st.pageID,
			AssistantMessageID: st.assistantMessageID,
			ToolCallID:         st.toolCallID,
			ParentSessionID:    st.parentSessionID,
			Idle:               &idle,
		}
		if st.held != nil {
			attachment.URL = st.held.TargetURL
			attachment.Width = st.held.Width
			attachment.Height = st.held.Height
			attachment.Title = pageTitle(st.held.Page)
		}
		out = append(out, attachment)
		projectIDs = append(projectIDs, st.projectID)
	}
	c.mu.Unlock()
	for i := range out {
		projected, ok := c.projectMetadata(
			ctx, projectIDs[i], out[i].SessionID, out[i].ParentSessionID,
			api.PreviewEvent{URL: out[i].URL, Title: out[i].Title},
		)
		if ok {
			out[i].URL, out[i].Title = projected.URL, projected.Title
		} else {
			out[i].URL, out[i].Title = "", ""
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].PageID < out[j].PageID
	})
	return out
}

// PublishDriving updates the page activity state.
func (c *Controller) PublishDriving(ctx context.Context, sessionID, pageID string, driving bool) {
	if c == nil {
		return
	}
	key := streamKey(strings.TrimSpace(sessionID), strings.TrimSpace(pageID))
	c.mu.Lock()
	st := c.pages[key]
	if st == nil {
		c.mu.Unlock()
		return
	}
	st.driving = driving
	idle := !driving
	projectID, eventSessionID, event := c.stampEventLocked(st, api.PreviewEvent{
		Op:   api.PreviewEventOpState,
		Idle: &idle,
	})
	c.mu.Unlock()
	c.publishEvent(ctx, projectID, eventSessionID, event)
}

// PublishAction overlays a driven action from the semantic driver action log.
func (c *Controller) PublishAction(ctx context.Context, sessionID, pageID string, act browser.CaptureAction, result json.RawMessage) {
	if c == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	pageID = strings.TrimSpace(pageID)
	key := streamKey(sessionID, pageID)
	c.mu.Lock()
	st := c.pages[key]
	var held *browser.HeldPage
	var assistantMessageID, toolCallID string
	if st != nil {
		held = st.held
		assistantMessageID = st.assistantMessageID
		toolCallID = st.toolCallID
	}
	c.mu.Unlock()
	if st == nil || held == nil || held.Page == nil {
		return
	}
	overlay := actionOverlay(act, result)
	if rect := resultRect(result); rect != nil {
		overlay.X, overlay.Y, overlay.W, overlay.H = &rect.X, &rect.Y, &rect.W, &rect.H
	}
	title := pageTitle(held.Page)
	c.mu.Lock()
	if c.pages[key] != st || st.assistantMessageID != assistantMessageID || st.toolCallID != toolCallID {
		c.mu.Unlock()
		return
	}
	idle := !st.driving
	projectID, eventSessionID, event := c.stampEventLocked(st, api.PreviewEvent{
		Op:              api.PreviewEventOpAction,
		ParentSessionID: st.parentSessionID,
		URL:             held.TargetURL,
		Title:           title,
		Idle:            &idle,
		Action:          overlay,
	})
	c.mu.Unlock()
	c.publishEvent(ctx, projectID, eventSessionID, event)
}

// Close tears down every stream.
func (c *Controller) Close(ctx context.Context) {
	if c == nil {
		return
	}
	var stops []func()
	c.mu.Lock()
	for key, st := range c.pages {
		stops = append(stops, c.stopCastLocked(ctx, st))
		delete(c.pages, key)
	}
	clear(c.watching)
	c.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
}

// DisposeSession tears down streams and watch state for one session.
func (c *Controller) DisposeSession(ctx context.Context, sessionID string) error {
	if c == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	type attachment struct{ sessionID, pageID string }
	var attachments []attachment
	c.mu.Lock()
	for key := range c.watching {
		if strings.HasPrefix(key, sessionID+"\x00") {
			delete(c.watching, key)
		}
	}
	for _, st := range c.pages {
		if st.sessionID == sessionID || st.parentSessionID == sessionID {
			attachments = append(attachments, attachment{sessionID: st.sessionID, pageID: st.pageID})
		}
	}
	c.mu.Unlock()
	for _, item := range attachments {
		c.Detach(ctx, item.sessionID, item.pageID)
	}
	return nil
}

func (c *Controller) startCast(ctx context.Context, st *stream) {
	if st == nil || st.held == nil || st.held.Page == nil {
		return
	}
	c.mu.Lock()
	if c.pages[streamKey(st.sessionID, st.pageID)] != st || st.cast != nil || !c.streamWatchedLocked(st) {
		c.mu.Unlock()
		return
	}
	castCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopPage := context.AfterFunc(st.held.Page.GetContext(), cancel) //nolint:contextcheck // Page teardown ends the cast independently of its opening request.
	h := &castHandle{cancel: func() { stopPage(); cancel() }, done: make(chan struct{})}
	st.cast = h
	page := st.held.Page.Context(castCtx)
	quality := c.cfg.JPEGQuality
	maxW := c.cfg.MaxWidth
	maxH := c.cfg.MaxHeight
	c.mu.Unlock()

	every := 1
	req := proto.PageStartScreencast{
		Format:        proto.PageStartScreencastFormatJpeg,
		Quality:       &quality,
		MaxWidth:      &maxW,
		MaxHeight:     &maxH,
		EveryNthFrame: &every,
	}
	startCtx, cancelStart := context.WithTimeout(castCtx, browser.RasterizeTimeout)
	err := req.Call(page.Context(startCtx))
	cancelStart()
	c.mu.Lock()
	current := st.cast == h
	if err != nil || !current {
		if current {
			st.cast = nil
		}
		c.mu.Unlock()
		h.cancel()
		close(h.done)
		if err == nil {
			// Cancellation can race screencast startup.
			stopPageCast(ctx, st.held.Page)
		}
		return
	}
	c.mu.Unlock()
	go c.castLoop(castCtx, st, page, h)
}

// stopCastLocked returns unlocked screencast cleanup.
func (c *Controller) stopCastLocked(ctx context.Context, st *stream) func() {
	h := st.cast
	if h == nil {
		return func() {}
	}
	st.cast = nil
	st.pending = nil
	var page *rod.Page
	if st.held != nil {
		page = st.held.Page
	}
	return func() {
		h.cancel()
		if page != nil {
			stopPageCast(ctx, page)
		}
		<-h.done
	}
}

func stopPageCast(parent context.Context, page *rod.Page) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
	defer cancel()
	_ = proto.PageStopScreencast{}.Call(page.Context(ctx))
}

func (c *Controller) castLoop(ctx context.Context, st *stream, page *rod.Page, h *castHandle) {
	defer close(h.done)
	wait := page.EachEvent(func(e *proto.PageScreencastFrame) {
		if e == nil {
			return
		}
		frameCtx, cancelFrame := context.WithTimeout(ctx, browser.RasterizeTimeout)
		defer cancelFrame()
		framePage := page.Context(frameCtx)
		_ = proto.PageScreencastFrameAck{SessionID: e.SessionID}.Call(framePage)
		if len(e.Data) == 0 || !c.acceptFrame(st, h) {
			return
		}
		url, title := pageMeta(framePage)
		regions, err := browser.CollectPageRegions(framePage)
		if err != nil {
			regions = browser.PageRegions{Complete: false}
		}
		c.offerFrame(frameCtx, st, h, e.Data, url, title, regions)
	})
	// Wait for the event subscriber before closing the cast.
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		wait()
	}()

	ticker := time.NewTicker(st.minInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			<-waitDone
			return
		case <-ticker.C:
			c.flushPending(ctx, st, h)
		}
	}
}

// acceptFrame rate-limits frame and geometry capture together.
func (c *Controller) acceptFrame(st *stream, h *castHandle) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st.cast != h {
		return false
	}
	if !st.lastAccept.IsZero() && time.Since(st.lastAccept) < st.minInterval/2 {
		return false
	}
	st.lastAccept = time.Now()
	return true
}

func (c *Controller) offerFrame(
	ctx context.Context, st *stream, h *castHandle, data []byte, url, title string,
	regions browser.PageRegions,
) {
	c.mu.Lock()
	if st.cast != h {
		c.mu.Unlock()
		return
	}
	if url == "" && st.held != nil {
		url = st.held.TargetURL
	}
	idle := !st.driving
	st.pending = &pendingFrame{
		data: append([]byte(nil), data...),
		url:  url, title: title, idle: &idle, regions: regions,
	}
	if st.held != nil {
		st.pending.width = st.held.Width
		st.pending.height = st.held.Height
	}
	ready := time.Since(st.lastPublish) >= st.minInterval
	var ev *api.PreviewEvent
	var pendingRegions browser.PageRegions
	if ready {
		ev, pendingRegions = c.takePendingLocked(st)
	}
	c.mu.Unlock()
	if ev != nil {
		c.publishFrame(ctx, st, *ev, pendingRegions)
	}
}

func (c *Controller) flushPending(ctx context.Context, st *stream, h *castHandle) {
	c.mu.Lock()
	if st.pending == nil || st.cast != h {
		c.mu.Unlock()
		return
	}
	if time.Since(st.lastPublish) < st.minInterval {
		c.mu.Unlock()
		return
	}
	ev, regions := c.takePendingLocked(st)
	c.mu.Unlock()
	if ev != nil {
		c.publishFrame(ctx, st, *ev, regions)
	}
}

func (c *Controller) publishFrame(
	ctx context.Context, st *stream, ev api.PreviewEvent, regions browser.PageRegions,
) {
	if st == nil {
		return
	}
	c.mu.Lock()
	projector := c.projector
	projectFrame := c.projectFrame
	c.mu.Unlock()
	if projector == nil || projectFrame == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(ev.JpegB64)
	if err != nil {
		return
	}
	safe, err := projectFrame(
		ctx, projector,
		captureprojection.ScopeFor(st.projectID, st.parentSessionID, st.sessionID),
		regions, "image/jpeg", raw,
	)
	if err != nil {
		return
	}
	ev.JpegB64 = base64.StdEncoding.EncodeToString(safe)
	if len(ev.JpegB64) > MaxJPEGB64Chars {
		return
	}
	c.publishEvent(ctx, st.projectID, st.sessionID, ev)
}

func (c *Controller) takePendingLocked(st *stream) (*api.PreviewEvent, browser.PageRegions) {
	if st.pending == nil {
		return nil, browser.PageRegions{}
	}
	pf := st.pending
	st.pending = nil
	b64 := base64.StdEncoding.EncodeToString(pf.data)
	if len(b64) > MaxJPEGB64Chars {
		return nil, browser.PageRegions{}
	}
	st.seq++
	st.lastPublish = time.Now()
	return &api.PreviewEvent{
		Op:                 api.PreviewEventOpFrame,
		SessionID:          st.sessionID,
		PageID:             st.pageID,
		AssistantMessageID: st.assistantMessageID,
		ToolCallID:         st.toolCallID,
		ParentSessionID:    st.parentSessionID,
		Seq:                st.seq,
		Mime:               "image/jpeg",
		JpegB64:            b64,
		Width:              pf.width,
		Height:             pf.height,
		URL:                pf.url,
		Title:              pf.title,
		Idle:               pf.idle,
	}, pf.regions
}

func (c *Controller) publishEvent(ctx context.Context, projectID, sessionID string, ev api.PreviewEvent) {
	if c == nil || c.publish == nil {
		return
	}
	projected, ok := c.projectMetadata(ctx, projectID, sessionID, ev.ParentSessionID, ev)
	if !ok {
		return
	}
	c.publish(ctx, projectID, sessionID, projected)
}

func (c *Controller) projectMetadata(
	ctx context.Context, projectID, sessionID, parentSessionID string, ev api.PreviewEvent,
) (api.PreviewEvent, bool) {
	if c == nil {
		return ev, true
	}
	c.mu.Lock()
	projector := c.projector
	c.mu.Unlock()
	if projector == nil {
		return api.PreviewEvent{}, false
	}
	scope := captureprojection.ScopeFor(projectID, parentSessionID, sessionID)
	for label, target := range map[string]*string{
		"url": &ev.URL, "title": &ev.Title,
	} {
		projected, err := projector.Text(ctx, scope, "capture.preview."+label, *target)
		if err != nil {
			return api.PreviewEvent{}, false
		}
		*target = projected.Value
	}
	if ev.Action != nil {
		action := *ev.Action
		for label, target := range map[string]*string{
			"action.label": &action.Label, "action.target": &action.Target,
		} {
			projected, err := projector.Text(ctx, scope, "capture.preview."+label, *target)
			if err != nil {
				return api.PreviewEvent{}, false
			}
			*target = projected.Value
		}
		ev.Action = &action
	}
	return ev, true
}

func (c *Controller) emit(ctx context.Context, st *stream, ev api.PreviewEvent) {
	if c == nil || st == nil {
		return
	}
	c.mu.Lock()
	projectID, sessionID, ev := c.stampEventLocked(st, ev)
	c.mu.Unlock()
	c.publishEvent(ctx, projectID, sessionID, ev)
}

func (c *Controller) stampEventLocked(st *stream, ev api.PreviewEvent) (string, string, api.PreviewEvent) {
	st.seq++
	ev.SessionID = st.sessionID
	ev.PageID = st.pageID
	ev.AssistantMessageID = st.assistantMessageID
	ev.ToolCallID = st.toolCallID
	ev.Seq = st.seq
	if ev.ParentSessionID == "" {
		ev.ParentSessionID = st.parentSessionID
	}
	projectID := st.projectID
	sessionID := st.sessionID
	return projectID, sessionID, ev
}

func (c *Controller) streamWatchedLocked(st *stream) bool {
	if st == nil {
		return false
	}
	return c.watching[streamKey(st.sessionID, st.pageID)] ||
		c.watching[streamKey(st.parentSessionID, st.pageID)]
}

func streamKey(sessionID, pageID string) string {
	return sessionID + "\x00" + pageID
}

func pageTitle(page *rod.Page) string {
	_, title := pageMeta(page)
	return title
}

func pageMeta(page *rod.Page) (url, title string) {
	if page == nil {
		return "", ""
	}
	info, err := browser.PageInfo(page)
	if err != nil || info == nil {
		return "", ""
	}
	return strings.TrimSpace(info.URL), strings.TrimSpace(info.Title)
}

type rect struct{ X, Y, W, H float64 }

// resultRect is the target box an action reported where its input landed.
func resultRect(result json.RawMessage) *rect {
	var parsed struct {
		Rect *struct {
			X, Y, Width, Height float64
		} `json:"rect"`
	}
	if json.Unmarshal(result, &parsed) != nil || parsed.Rect == nil || parsed.Rect.Width <= 0 || parsed.Rect.Height <= 0 {
		return nil
	}
	return &rect{X: parsed.Rect.X, Y: parsed.Rect.Y, W: parsed.Rect.Width, H: parsed.Rect.Height}
}

func actionOverlay(act browser.CaptureAction, result json.RawMessage) *api.PreviewActionOverlay {
	typ := strings.ToLower(strings.TrimSpace(act.Type))
	if typ == "" && len(result) > 0 {
		var parsed map[string]any
		if json.Unmarshal(result, &parsed) == nil {
			if t, ok := parsed["type"].(string); ok {
				typ = strings.ToLower(strings.TrimSpace(t))
			}
		}
	}
	target := firstNonEmpty(
		strings.TrimSpace(act.Selector),
		strings.TrimSpace(act.Testid),
		strings.TrimSpace(act.Label),
		strings.TrimSpace(act.Text),
		strings.TrimSpace(act.Role),
	)
	label := typ
	if label == "" {
		label = "action"
	}
	if target != "" {
		label = label + " · " + target
	}
	return &api.PreviewActionOverlay{Label: label, Target: target}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
