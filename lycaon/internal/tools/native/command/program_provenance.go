package command

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
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
	custodyTrees         map[string][]string
	clearedFiles         map[string]bool
	activeTrackers       []*opProvenanceTracker
}

type opProvenanceTracker struct {
	mu        sync.Mutex
	exchanges []evidence.ExchangeProvenance
	readPaths []string
	// networkURLs are destinations an external process of the op reached;
	// what it read from them may be in any stream it wrote.
	networkURLs []string
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

// addNetworkRead records destinations an external process of the op reached.
func (t *opProvenanceTracker) addNetworkRead(urls []string) {
	if t == nil || len(urls) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.networkURLs = appendUniqueProvenanceStrings(t.networkURLs, urls...)
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
	roots := r.tc.Source.Roots
	if abs, ref, err := projectroot.ResolveAbs(roots, r.tc.Source.ActiveRootID, display); err == nil && !ref.External {
		return projectroot.Qualify(roots, r.tc.Source.ActiveRootID, ref, abs)
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

// observeContentFlow records an owner's read of stored content for the op's
// streams, and carries the custody of what it read to files it derived from it.
func (r *programRun) observeContentFlow(ctx context.Context, tracker *opProvenanceTracker, flow evidence.ContentFlow) {
	if r == nil || tracker == nil {
		return
	}
	source := r.normalizeProvenancePath("", flow.Source)
	if source == "" {
		return
	}
	tracker.addReadPath(source)
	if len(flow.Files) == 0 {
		return
	}
	urls := r.lookupCustodyURLs(ctx, []string{source})
	for _, file := range flow.Files {
		r.addCustodyFile(r.normalizeProvenancePath("", file), urls)
	}
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
	networkURLs := append([]string(nil), tracker.networkURLs...)
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
		for _, dir := range ex.WrittenTrees {
			r.addCustodyTree(r.normalizeProvenancePath("", dir), ex.ObservedURLs)
		}
		if ex.WroteStdout {
			r.routeExternalStream(outClass, ex.ObservedURLs)
		}
		if ex.WroteStderr {
			r.routeExternalStream(errClass, ex.ObservedURLs)
		}
	}

	// Content read from custody files or by an external process from the
	// network reaches whichever streams the op wrote.
	var streamURLs []string
	if len(readPaths) > 0 {
		streamURLs = r.lookupCustodyURLs(ctx, readPaths)
	}
	streamURLs = appendUniqueProvenanceStrings(streamURLs, networkURLs...)
	if len(streamURLs) > 0 {
		if outWrote {
			r.routeExternalStream(outClass, streamURLs)
		}
		if errWrote {
			r.routeExternalStream(errClass, streamURLs)
		}
	}

	if outClass.redirectPath != "" && len(opExternalURLs) == 0 && len(streamURLs) == 0 && outWrote {
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

// addCustodyTree records a directory the program filled from the network.
func (r *programRun) addCustodyTree(normPath string, urls []string) {
	if normPath == "" || len(urls) == 0 {
		return
	}
	r.provenance.mu.Lock()
	defer r.provenance.mu.Unlock()
	if r.provenance.custodyTrees == nil {
		r.provenance.custodyTrees = make(map[string][]string)
	}
	r.provenance.custodyTrees[normPath] = appendUniqueProvenanceStrings(r.provenance.custodyTrees[normPath], urls...)
}

// treeCustodyLocked is the custody of the nearest tree this program filled
// that holds normPath. Callers hold r.provenance.mu.
func (r *programRun) treeCustodyLocked(normPath string) []string {
	for dir := normPath; dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
		if urls := r.provenance.custodyTrees[dir]; len(urls) > 0 {
			return urls
		}
	}
	return nil
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
		} else if !r.provenance.clearedFiles[p] {
			urls = appendUniqueProvenanceStrings(urls, r.treeCustodyLocked(p)...)
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
	var custodyTrees []string
	for p, urls := range run.provenance.custodyTrees {
		custodyTrees = append(custodyTrees, p)
		observedURLs = appendUniqueProvenanceStrings(observedURLs, urls...)
	}
	sort.Strings(custodyTrees)
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

	if !ingested && len(custodyPaths) == 0 && len(custodyTrees) == 0 && len(clearedPaths) == 0 {
		return
	}
	prov := evidence.ProgramHTTPProvenance{
		ProgramID:        run.root.Identity.ProgramID,
		UntrustedContent: ingested,
		ObservedURLs:     observedURLs,
		CustodyPaths:     custodyPaths,
		CustodyTrees:     custodyTrees,
		ClearedPaths:     clearedPaths,
	}
	_, _ = store.CommitProgramHTTPProvenance(commitCtx, run.tc.Identity.SessionID, projDir, prov)
	if chatID := run.tc.ChatSessionID(); chatID != "" && chatID != run.tc.Identity.SessionID && (len(custodyPaths) > 0 || len(custodyTrees) > 0 || len(clearedPaths) > 0) {
		_, _ = store.CommitProgramHTTPProvenance(commitCtx, chatID, projDir, evidence.ProgramHTTPProvenance{
			ProgramID:        run.root.Identity.ProgramID,
			UntrustedContent: false,
			ObservedURLs:     observedURLs,
			CustodyPaths:     custodyPaths,
			CustodyTrees:     custodyTrees,
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
