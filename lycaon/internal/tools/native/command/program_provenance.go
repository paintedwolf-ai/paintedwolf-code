package command

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
)

type programHTTPProvenanceStore interface {
	CommitProgramHTTPProvenance(ctx context.Context, sessionID, projectDir string, prov evidence.ProgramHTTPProvenance) (string, error)
	ExternalCustodyURLsForPaths(ctx context.Context, sessionID, projectDir string, paths []string) ([]string, error)
}

type programProvenanceState struct {
	mu                   sync.Mutex
	directExternalOutput bool
	pipedExternalOutput  bool
	externalURLs         []string
	custodyFiles         map[string][]string
	clearedFiles         map[string]bool
	activeTrackers       []*opProvenanceTracker
}

type opProvenanceTracker struct {
	mu        sync.Mutex
	exchanges []evidence.ExchangeProvenance
	readPaths []string
}

type opTrackerContextKey struct{}

func withOpTracker(ctx context.Context, t *opProvenanceTracker) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, opTrackerContextKey{}, t)
}

func opTrackerFromContext(ctx context.Context) *opProvenanceTracker {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(opTrackerContextKey{}).(*opProvenanceTracker)
	return t
}

func (t *opProvenanceTracker) addExchange(ex evidence.ExchangeProvenance) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.exchanges = append(t.exchanges, ex)
}

func (t *opProvenanceTracker) addReadPath(path string) {
	if t == nil || path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !slices.Contains(t.readPaths, path) {
		t.readPaths = append(t.readPaths, path)
	}
}

func (r *programRun) projectDir() string {
	if r == nil {
		return ""
	}
	for _, attached := range r.tc.Source.Roots {
		if attached.ID == r.tc.Source.ActiveRootID && attached.Path != "" {
			return attached.Path
		}
	}
	if len(r.tc.Source.Roots) > 0 {
		return r.tc.Source.Roots[0].Path
	}
	return ""
}

func (r *programRun) normalizeProvenancePath(root, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	display := path
	if root != "" {
		for _, attached := range r.tc.Source.Roots {
			if attached.ID == root {
				display = filepath.Join(attached.Path, filepath.FromSlash(path))
				break
			}
		}
	}
	if norm := evidence.NormalizeEvidencePath(r.projectDir(), display); norm != "" {
		return norm
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func (r *programRun) observeProvenanceFileRead(ctx context.Context, root, path string) {
	if r == nil {
		return
	}
	norm := r.normalizeProvenancePath(root, path)
	if norm == "" {
		return
	}
	if t := opTrackerFromContext(ctx); t != nil {
		t.addReadPath(norm)
	}
	r.provenance.mu.Lock()
	if n := len(r.provenance.activeTrackers); n > 0 {
		r.provenance.activeTrackers[n-1].addReadPath(norm)
	}
	r.provenance.mu.Unlock()
}

func (r *programRun) noteInputRedirectRead(tracker *opProvenanceTracker, normPath string) {
	if r == nil || normPath == "" {
		return
	}
	if tracker != nil {
		tracker.addReadPath(normPath)
	}
	r.provenance.mu.Lock()
	if n := len(r.provenance.activeTrackers); n > 0 {
		r.provenance.activeTrackers[n-1].addReadPath(normPath)
		r.provenance.mu.Unlock()
		return
	}
	r.provenance.mu.Unlock()
	if urls := r.lookupCustodyURLs(context.Background(), []string{normPath}); len(urls) > 0 {
		r.markPipedExternalOutput(urls)
	}
}

func (r *programRun) beginOpTracker() *opProvenanceTracker {
	t := &opProvenanceTracker{}
	r.provenance.mu.Lock()
	r.provenance.activeTrackers = append(r.provenance.activeTrackers, t)
	r.provenance.mu.Unlock()
	return t
}

func (r *programRun) wrapProducerStreams(ctx context.Context, tracker *opProvenanceTracker, outClass, errClass outputStreamClass, streams commandprogram.Streams) (commandprogram.Streams, func()) {
	outCounter := &countingStreamWriter{w: nonNilWriter(streams.Output)}
	errCounter := &countingStreamWriter{w: nonNilWriter(streams.Error)}
	streams.Output = outCounter
	streams.Error = errCounter
	return streams, func() {
		r.finishOpTracker(ctx, tracker, outClass, outCounter.bytes.Load() > 0, errClass, errCounter.bytes.Load() > 0)
	}
}

func (r *programRun) finishOpTracker(ctx context.Context, tracker *opProvenanceTracker, outClass outputStreamClass, outWrote bool, errClass outputStreamClass, errWrote bool) {
	if r == nil || tracker == nil {
		return
	}
	r.provenance.mu.Lock()
	for i := len(r.provenance.activeTrackers) - 1; i >= 0; i-- {
		if r.provenance.activeTrackers[i] == tracker {
			r.provenance.activeTrackers = append(r.provenance.activeTrackers[:i], r.provenance.activeTrackers[i+1:]...)
			break
		}
	}
	r.provenance.mu.Unlock()

	tracker.mu.Lock()
	exchanges := append([]evidence.ExchangeProvenance(nil), tracker.exchanges...)
	readPaths := append([]string(nil), tracker.readPaths...)
	tracker.mu.Unlock()

	var opExternalURLs []string
	for _, ex := range exchanges {
		if !ex.External || len(ex.ObservedURLs) == 0 {
			for _, f := range ex.WrittenFiles {
				r.clearCustodyFile(r.normalizeProvenancePath("", f))
			}
			continue
		}
		opExternalURLs = appendUniqueProvenanceStrings(opExternalURLs, ex.ObservedURLs...)
		for _, f := range ex.WrittenFiles {
			r.addCustodyFile(r.normalizeProvenancePath("", f), ex.ObservedURLs)
		}
		if ex.WroteStdout {
			r.routeExternalStream(outClass, ex.ObservedURLs)
		}
		if ex.WroteStderr {
			r.routeExternalStream(errClass, ex.ObservedURLs)
		}
	}

	var readCustodyURLs []string
	if len(readPaths) > 0 {
		readCustodyURLs = r.lookupCustodyURLs(ctx, readPaths)
		if len(readCustodyURLs) > 0 {
			if outWrote {
				r.routeExternalStream(outClass, readCustodyURLs)
			}
			if errWrote {
				r.routeExternalStream(errClass, readCustodyURLs)
			}
		}
	}

	if outClass.redirectPath != "" && len(opExternalURLs) == 0 && len(readCustodyURLs) == 0 && outWrote {
		if pipedURLs := r.pipedExternalURLs(); len(pipedURLs) > 0 {
			r.addCustodyFile(outClass.redirectPath, pipedURLs)
		} else if outClass.redirectOperator == ">" || outClass.redirectOperator == "&>" {
			r.clearCustodyFile(outClass.redirectPath)
		}
	}
}

func (r *programRun) routeExternalStream(cls outputStreamClass, urls []string) {
	if len(urls) == 0 {
		return
	}
	switch {
	case cls.topLevel:
		r.markDirectExternalOutput(urls)
	case cls.redirectPath != "":
		r.addCustodyFile(cls.redirectPath, urls)
	case cls.piped:
		r.markPipedExternalOutput(urls)
	}
}

func (r *programRun) markDirectExternalOutput(urls []string) {
	r.provenance.mu.Lock()
	defer r.provenance.mu.Unlock()
	r.provenance.directExternalOutput = true
	r.provenance.externalURLs = appendUniqueProvenanceStrings(r.provenance.externalURLs, urls...)
}

func (r *programRun) markPipedExternalOutput(urls []string) {
	r.provenance.mu.Lock()
	defer r.provenance.mu.Unlock()
	r.provenance.pipedExternalOutput = true
	r.provenance.externalURLs = appendUniqueProvenanceStrings(r.provenance.externalURLs, urls...)
}

func (r *programRun) pipedExternalURLs() []string {
	r.provenance.mu.Lock()
	defer r.provenance.mu.Unlock()
	if !r.provenance.pipedExternalOutput {
		return nil
	}
	return append([]string(nil), r.provenance.externalURLs...)
}

func (r *programRun) addCustodyFile(normPath string, urls []string) {
	if normPath == "" || len(urls) == 0 {
		return
	}
	r.provenance.mu.Lock()
	defer r.provenance.mu.Unlock()
	if r.provenance.custodyFiles == nil {
		r.provenance.custodyFiles = make(map[string][]string)
	}
	if r.provenance.clearedFiles != nil {
		delete(r.provenance.clearedFiles, normPath)
	}
	r.provenance.custodyFiles[normPath] = appendUniqueProvenanceStrings(r.provenance.custodyFiles[normPath], urls...)
}

func (r *programRun) clearCustodyFile(normPath string) {
	if normPath == "" {
		return
	}
	r.provenance.mu.Lock()
	defer r.provenance.mu.Unlock()
	if r.provenance.custodyFiles != nil {
		delete(r.provenance.custodyFiles, normPath)
	}
	if r.provenance.clearedFiles == nil {
		r.provenance.clearedFiles = make(map[string]bool)
	}
	r.provenance.clearedFiles[normPath] = true
}

func (r *programRun) lookupCustodyURLs(ctx context.Context, normPaths []string) []string {
	var urls []string
	var storeQueryPaths []string
	r.provenance.mu.Lock()
	for _, p := range normPaths {
		if p == "" {
			continue
		}
		if inProg, ok := r.provenance.custodyFiles[p]; ok && len(inProg) > 0 {
			urls = appendUniqueProvenanceStrings(urls, inProg...)
		}
		if r.provenance.clearedFiles == nil || !r.provenance.clearedFiles[p] {
			storeQueryPaths = appendUniqueProvenanceStrings(storeQueryPaths, p)
		}
	}
	r.provenance.mu.Unlock()

	store, ok := r.sessions.(programHTTPProvenanceStore)
	if !ok || len(storeQueryPaths) == 0 {
		return urls
	}
	projDir := r.projectDir()
	sessionIDs := []string{r.tc.Identity.SessionID}
	if chatID := r.tc.ChatSessionID(); chatID != "" && chatID != r.tc.Identity.SessionID {
		sessionIDs = append(sessionIDs, chatID)
	}
	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		if found, err := store.ExternalCustodyURLsForPaths(ctx, sid, projDir, storeQueryPaths); err == nil && len(found) > 0 {
			urls = appendUniqueProvenanceStrings(urls, found...)
		}
	}
	return urls
}

func (r *programRun) noteNativeFileMutation(ctx context.Context, commandName string, targets []commandprogram.PathTarget) {
	if r == nil || len(targets) == 0 {
		return
	}
	switch commandName {
	case "rm":
		for _, t := range targets {
			r.clearCustodyFile(r.normalizeProvenancePath("", t.Path))
		}
	case "cp", "mv":
		var srcPaths, dstPaths []string
		for _, t := range targets {
			norm := r.normalizeProvenancePath("", t.Path)
			if norm == "" {
				continue
			}
			if t.Write {
				dstPaths = appendUniqueProvenanceStrings(dstPaths, norm)
			} else {
				srcPaths = appendUniqueProvenanceStrings(srcPaths, norm)
			}
		}
		if len(dstPaths) == 0 && len(targets) >= 2 {
			if last := r.normalizeProvenancePath("", targets[len(targets)-1].Path); last != "" {
				dstPaths = []string{last}
				srcPaths = nil
				for _, t := range targets[:len(targets)-1] {
					if norm := r.normalizeProvenancePath("", t.Path); norm != "" {
						srcPaths = appendUniqueProvenanceStrings(srcPaths, norm)
					}
				}
			}
		}
		srcURLs := r.lookupCustodyURLs(ctx, srcPaths)
		if commandName == "mv" {
			for _, src := range srcPaths {
				if !slices.Contains(dstPaths, src) {
					r.clearCustodyFile(src)
				}
			}
		}
		for _, dst := range dstPaths {
			if len(srcURLs) > 0 {
				r.addCustodyFile(dst, srcURLs)
			} else {
				r.clearCustodyFile(dst)
			}
		}
	}
}

func (r *ProgramRuntime) commitProgramHTTPProvenance(ctx context.Context, run *programRun, hasModelVisibleOutput bool) {
	if r == nil || run == nil || r.Sessions == nil {
		return
	}
	store, ok := r.Sessions.(programHTTPProvenanceStore)
	if !ok {
		return
	}
	run.provenance.mu.Lock()
	ingested := run.provenance.directExternalOutput || (run.provenance.pipedExternalOutput && hasModelVisibleOutput)
	var observedURLs []string
	if ingested {
		observedURLs = appendUniqueProvenanceStrings(observedURLs, run.provenance.externalURLs...)
	}
	var custodyPaths []string
	for p, urls := range run.provenance.custodyFiles {
		if len(urls) == 0 {
			continue
		}
		custodyPaths = append(custodyPaths, p)
		observedURLs = appendUniqueProvenanceStrings(observedURLs, urls...)
	}
	sort.Strings(custodyPaths)
	var clearedCandidates []string
	for p, cleared := range run.provenance.clearedFiles {
		if cleared && p != "" {
			clearedCandidates = append(clearedCandidates, p)
		}
	}
	sort.Strings(clearedCandidates)
	run.provenance.mu.Unlock()

	commitCtx := context.WithoutCancel(ctx)
	projDir := run.projectDir()
	var clearedPaths []string
	for _, p := range clearedCandidates {
		if existing, err := store.ExternalCustodyURLsForPaths(commitCtx, run.tc.Identity.SessionID, projDir, []string{p}); err == nil && len(existing) > 0 {
			clearedPaths = append(clearedPaths, p)
			continue
		}
		if chatID := run.tc.ChatSessionID(); chatID != "" && chatID != run.tc.Identity.SessionID {
			if existing, err := store.ExternalCustodyURLsForPaths(commitCtx, chatID, projDir, []string{p}); err == nil && len(existing) > 0 {
				clearedPaths = append(clearedPaths, p)
			}
		}
	}

	if !ingested && len(custodyPaths) == 0 && len(clearedPaths) == 0 {
		return
	}
	prov := evidence.ProgramHTTPProvenance{
		ProgramID:        run.root.Identity.ProgramID,
		UntrustedContent: ingested,
		ObservedURLs:     observedURLs,
		CustodyPaths:     custodyPaths,
		ClearedPaths:     clearedPaths,
	}
	_, _ = store.CommitProgramHTTPProvenance(commitCtx, run.tc.Identity.SessionID, projDir, prov)
	if chatID := run.tc.ChatSessionID(); chatID != "" && chatID != run.tc.Identity.SessionID && (len(custodyPaths) > 0 || len(clearedPaths) > 0) {
		_, _ = store.CommitProgramHTTPProvenance(commitCtx, chatID, projDir, evidence.ProgramHTTPProvenance{
			ProgramID:        run.root.Identity.ProgramID,
			UntrustedContent: false,
			ObservedURLs:     observedURLs,
			CustodyPaths:     custodyPaths,
			ClearedPaths:     clearedPaths,
		})
	}
}

func appendUniqueProvenanceStrings(dst []string, values ...string) []string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(dst, v) {
			continue
		}
		dst = append(dst, v)
	}
	return dst
}
