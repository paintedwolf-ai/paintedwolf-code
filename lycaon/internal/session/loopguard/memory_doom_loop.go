package loopguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type responseCounts struct {
	count       int
	lastCode    string
	sameCodeRun int
}

type doomLoopState struct {
	responseCounts
	responseID     string
	beforeResponse responseCounts
	// seenEffectSeq is the session effect sequence at this bucket's last attempt.
	seenEffectSeq    int64
	readDocumentID   string
	pendingProgress  bool
	seenProgressSeq  int64
	progressRecovery progressRecovery
}

// sessionEffects tracks consecutive effectful calls. Read-only calls preserve the count.
type sessionEffects struct {
	// seq counts effectful invocations in this session.
	seq int64
	// key is the doom-loop key of the most recent effectful invocation.
	key         string
	progressSeq int64
}

// MemoryDoomLoopGuard keeps bounded per-session counters in memory.
type MemoryDoomLoopGuard struct {
	mu              sync.RWMutex
	reviewIntent    func(context.Context, string) (string, error)
	progressState   func(context.Context, string) (progress.PrerequisiteState, error)
	intents         scopedstore.LRU[string]
	responseIntents scopedstore.LRU[string]
	// pageTarget groups live handles by target; unresolved handles retain their ids.
	pageTarget func(sessionID, pageID string) string
	// states counts responses with identical invocations per session.
	states scopedstore.LRU[map[string]*doomLoopState]
	// fruitless groups empty searches by question across scope changes.
	fruitless scopedstore.LRU[map[string]int]
	// codeRuns counts rejected responses by tool and code across argument changes.
	codeRuns scopedstore.LRU[map[string]rejectedResponses]
	// effects distinguishes repetition from calls made after another mutation.
	effects scopedstore.LRU[sessionEffects]
}

// NewMemoryDoomLoopGuard starts with empty counters.
func NewMemoryDoomLoopGuard() *MemoryDoomLoopGuard {
	return &MemoryDoomLoopGuard{}
}

// SetPageTargetResolver wires live-page id → target resolution for fingerprints.
func (g *MemoryDoomLoopGuard) SetPageTargetResolver(fn func(sessionID, pageID string) string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pageTarget = fn
}

func (g *MemoryDoomLoopGuard) resolvePageTarget(sessionID, pageID string) string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	fn := g.pageTarget
	g.mu.RUnlock()
	if fn == nil {
		return ""
	}
	return fn(sessionID, pageID)
}

func (g *MemoryDoomLoopGuard) doomLoopKey(sessionID, tool string, args map[string]any) (string, error) {
	finger := doomLoopFingerprintArgs(tool, args)
	if target, ok := doomLoopPageTargetArg(tool, args, func(pageID string) string {
		return g.resolvePageTarget(sessionID, pageID)
	}); ok {
		// Copy: the default fingerprint branch hands back the caller's own args map.
		swapped := make(map[string]any, len(finger)+1)
		for k, v := range finger {
			if k == "id" {
				continue
			}
			swapped[k] = v
		}
		swapped["page_target"] = target
		finger = swapped
	}
	b, err := json.Marshal(finger)
	if err != nil {
		return "", err
	}
	payload := append([]byte(strings.ToLower(strings.TrimSpace(tool))), 0)
	payload = append(payload, b...)
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:8]), nil
}

// Check blocks repeated identical calls or consecutive rejects sharing a code.
func (g *MemoryDoomLoopGuard) Check(ctx context.Context, sessionID, responseID, tool string, args map[string]any, offered bool, reads *tools.AgentReadBases) (allowed bool, count int, repeatedCode string, err error) {
	if err := ctx.Err(); err != nil {
		return false, 0, "", err
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return true, 0, "", nil
	}
	if err := g.refreshReviewIntent(ctx, sessionID); err != nil {
		return false, 0, "", err
	}
	key, err := g.doomLoopKey(sessionID, tool, args)
	if err != nil {
		return false, 0, "", err
	}
	resolvedProgressCode, err := g.resolvedProgressPrerequisite(ctx, sessionID, key)
	if err != nil {
		return false, 0, "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if intent, known := g.intents.Load(sessionID); known {
		g.responseIntents.Store(sessionID+"\x00"+responseID, intent)
	}
	sess, _ := g.states.Load(sessionID)
	st := sess[key]
	if st != nil {
		resolved := st.lastCode == "TOOL_NOT_OFFERED" && offered ||
			strings.EqualFold(strings.TrimSpace(tool), "update_progress") && st.progressRecovery.matches(args) ||
			resolvedProgressCode != "" && st.lastCode == resolvedProgressCode
		if st.lastCode == "EDITOR_DOCUMENT_READ_REQUIRED" && st.readDocumentID != "" {
			basis, known := reads.Basis(st.readDocumentID)
			resolved = known && basis.Revision > 0 && (basis.Whole || len(basis.Spans) > 0)
		}
		if resolved {
			delete(sess, key)
			st = nil
		}
	}
	if st == nil {
		return true, 0, "", nil
	}
	if g.supersededLocked(sessionID, key, st) {
		return true, 0, "", nil
	}
	counts := st.responseCounts
	if st.responseID == responseID {
		counts = st.beforeResponse
	}
	if counts.lastCode != "" && counts.sameCodeRun >= DoomLoopMaxSameCodeRejects {
		return false, counts.count, counts.lastCode, nil
	}
	return counts.count < DoomLoopMaxAttempts, counts.count, "", nil
}

// Pending progress retains its no-op history across unrelated mutations.
func (g *MemoryDoomLoopGuard) supersededLocked(sessionID, key string, st *doomLoopState) bool {
	effects, _ := g.effects.Load(sessionID)
	if st.pendingProgress {
		return effects.progressSeq > st.seenProgressSeq && effects.key != key
	}
	return effects.seq > st.seenEffectSeq && effects.key != key
}

// RecordAttempt tracks responses; mutated marks an effect that renews repetition context.
func (g *MemoryDoomLoopGuard) RecordAttempt(
	ctx context.Context,
	sessionID, responseID, tool string,
	args map[string]any,
	failure *api.InvocationFailure,
	mutated bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(responseID) == "" {
		return fmt.Errorf("record tool attempt: response id required")
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	if err := g.refreshReviewIntent(ctx, sessionID); err != nil {
		return err
	}
	key, err := g.doomLoopKey(sessionID, tool, args)
	if err != nil {
		return err
	}
	rejectCode := ""
	if failure != nil {
		rejectCode = strings.TrimSpace(failure.Code)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// An older response cannot rebuild counters after fresh direction.
	if admitted, known := g.responseIntents.Load(sessionID + "\x00" + responseID); known {
		current, _ := g.intents.Load(sessionID)
		if admitted != current {
			return nil
		}
	}
	sess, ok := g.states.Load(sessionID)
	if !ok || sess == nil {
		sess = make(map[string]*doomLoopState)
	}
	st := sess[key]
	if st == nil {
		content, _ := args["content"].(string)
		st = &doomLoopState{pendingProgress: strings.EqualFold(strings.TrimSpace(tool), "update_progress") && !progress.AllTerminal(content)}
		sess[key] = st
	}
	g.states.Store(sessionID, sess)
	g.recordCodeRejectLocked(sessionID, responseID, tool, rejectCode)
	if g.supersededLocked(sessionID, key, st) {
		*st = doomLoopState{pendingProgress: st.pendingProgress}
	}
	effects, _ := g.effects.Load(sessionID)
	st.seenEffectSeq = effects.seq
	st.seenProgressSeq = effects.progressSeq
	if mutated {
		effects.seq++
		if strings.EqualFold(strings.TrimSpace(tool), "update_progress") {
			effects.progressSeq++
			st.seenProgressSeq = effects.progressSeq
		}
		effects.key = key
		st.seenEffectSeq = effects.seq
		g.effects.Store(sessionID, effects)
	}
	ignoredSuccess := st.responseID == responseID && rejectCode == ""
	st.recordResponse(responseID, rejectCode)
	if ignoredSuccess {
		return nil
	}
	st.progressRecovery = progressRecovery{}
	if strings.EqualFold(strings.TrimSpace(tool), "update_progress") {
		st.progressRecovery = progressRecoveryFromFailure(failure)
	}
	st.readDocumentID = ""
	if rejectCode == "EDITOR_DOCUMENT_READ_REQUIRED" && failure != nil {
		st.readDocumentID, _ = failure.Details["document_id"].(string)
	}
	return nil
}

// Calls in one response cannot react to each other's feedback.
func (st *doomLoopState) recordResponse(responseID, rejectCode string) {
	if st.responseID != responseID {
		st.beforeResponse = st.responseCounts
		st.responseID = responseID
		st.count++
	} else if rejectCode == "" {
		return
	}
	switch rejectCode {
	case "":
		st.sameCodeRun = 0
	case st.beforeResponse.lastCode:
		st.sameCodeRun = st.beforeResponse.sameCodeRun + 1
	default:
		st.sameCodeRun = 1
	}
	st.lastCode = rejectCode
}

// doomLoopCodeKey identifies one tool's rejections under one structured Code.
func doomLoopCodeKey(tool, code string) string {
	return strings.ToLower(strings.TrimSpace(tool)) + "\x00" + strings.TrimSpace(code)
}

// CodeRejectResponses counts rejected responses up to the escalation threshold.
func (g *MemoryDoomLoopGuard) CodeRejectResponses(sessionID, tool, code string) int {
	if g == nil || strings.TrimSpace(code) == "" {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	totals, _ := g.codeRuns.Load(sessionID)
	return len(totals[doomLoopCodeKey(tool, code)])
}

type rejectedResponses map[string]struct{}

// Each response contributes once per tool and code. Caller holds mu.
func (g *MemoryDoomLoopGuard) recordCodeRejectLocked(sessionID, responseID, tool, code string) {
	if strings.TrimSpace(code) == "" {
		return
	}
	totals, ok := g.codeRuns.Load(sessionID)
	if !ok || totals == nil {
		totals = make(map[string]rejectedResponses)
	}
	key := doomLoopCodeKey(tool, code)
	entry := totals[key]
	if len(entry) >= DoomLoopMaxCodeRepeats {
		return
	}
	if entry == nil {
		entry = make(rejectedResponses)
	}
	entry[responseID] = struct{}{}
	totals[key] = entry
	g.codeRuns.Store(sessionID, totals)
}

// RecordSearchOutcome counts fruitless searches by question, excluding scope changes.
func (g *MemoryDoomLoopGuard) RecordSearchOutcome(ctx context.Context, sessionID, tool string, args map[string]any, foundMaterial bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	question, ok := doomLoopQuestionKey(tool, args)
	if !ok {
		return 0, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	runs, ok := g.fruitless.Load(sessionID)
	if !ok || runs == nil {
		runs = make(map[string]int)
	}
	if foundMaterial {
		delete(runs, question)
		g.fruitless.Store(sessionID, runs)
		return 0, nil
	}
	runs[question]++
	g.fruitless.Store(sessionID, runs)
	return runs[question], nil
}

// FruitlessSearchRun reads the question's current count without recording an attempt.
func (g *MemoryDoomLoopGuard) FruitlessSearchRun(sessionID, tool string, args map[string]any) int {
	question, ok := doomLoopQuestionKey(tool, args)
	if !ok {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	runs, _ := g.fruitless.Load(sessionID)
	return runs[question]
}
