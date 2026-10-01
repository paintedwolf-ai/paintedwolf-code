package loopguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/scopedstore"
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
	seenEffectSeq int64
}

// sessionEffects tracks consecutive effectful calls. Read-only calls preserve the count.
type sessionEffects struct {
	// seq counts effectful invocations in this session.
	seq int64
	// key is the doom-loop key of the most recent effectful invocation.
	key string
}

// MemoryDoomLoopGuard keeps bounded per-session counters in memory.
type MemoryDoomLoopGuard struct {
	mu sync.RWMutex
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

// doomLoopQuestionArgs identifies a search question independently of scope and pagination.
var doomLoopQuestionArgs = map[string][]string{
	"grep": {"pattern", "case_insensitive", "structural", "lang"},
	"find": {"name_glob", "pattern", "type"},
	// recall splits the same way: query is the question, widen and limit scope it.
	"recall": {"query"},
}

// doomLoopQuestionKey excludes scope and pagination from tracked search identity.
func doomLoopQuestionKey(tool string, args map[string]any) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(tool))
	keys, ok := doomLoopQuestionArgs[name]
	if !ok {
		return "", false
	}
	question := make(map[string]any, len(keys))
	for _, key := range keys {
		if v, present := args[key]; present {
			question[key] = v
		}
	}
	if len(question) == 0 {
		return "", false
	}
	b, err := json.Marshal(question)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(append([]byte(name), b...))
	return fmt.Sprintf("%x", sum[:8]), true
}

// doomLoopFingerprintArgs excludes display copy and idle waits from invocation identity.
func doomLoopFingerprintArgs(tool string, args map[string]any) map[string]any {
	name := strings.ToLower(strings.TrimSpace(tool))
	switch name {
	case "summarize":
		if args == nil {
			return map[string]any{}
		}
		out := make(map[string]any, 4)
		for _, key := range []string{"path", "paths", "content", "pattern"} {
			if v, ok := args[key]; ok {
				out[key] = v
			}
		}
		return out
	case "capture_page", "measure_page", "page_act", "page_snapshot":
		return doomLoopVisualDriveArgs(args)
	case "render_view":
		return doomLoopRenderViewArgs(args)
	default:
		return args
	}
}

// doomLoopPageTargetArg groups reopened pages by target, using ids for unresolved handles.
func doomLoopPageTargetArg(tool string, args map[string]any, resolve func(string) string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "page_snapshot", "page_act", "page_close":
	default:
		return "", false
	}
	pageID, _ := args["id"].(string)
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return "", false
	}
	if resolve != nil {
		if target := strings.TrimSpace(resolve(pageID)); target != "" {
			return target, true
		}
	}
	return pageID, true
}

// doomLoopVisualDriveArgs retains the target and material actions, excluding captions and waits.
func doomLoopVisualDriveArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, 6)
	for _, key := range []string{"url", "project_dir", "selector", "viewport", "selectors"} {
		if v, ok := args[key]; ok {
			out[key] = v
		}
	}
	if actions, ok := args["actions"]; ok {
		if material := doomLoopMaterialActions(actions); material != nil {
			out["actions"] = material
		}
	}
	return out
}

func doomLoopRenderViewArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, 4)
	for _, key := range []string{"markup", "html", "svg", "viewport"} {
		if v, ok := args[key]; ok {
			out[key] = v
		}
	}
	return out
}

func doomLoopMaterialActions(raw any) any {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return raw
	}
	for _, step := range list {
		m, ok := step.(map[string]any)
		if !ok {
			return raw
		}
		typ, _ := m["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(typ), "wait") {
			return raw
		}
	}
	// Pure wait scripts are equivalent to a bare snapshot of the same target.
	return nil
}

// Check blocks repeated identical calls or consecutive rejects sharing a code.
func (g *MemoryDoomLoopGuard) Check(ctx context.Context, sessionID, responseID, tool string, args map[string]any) (allowed bool, count int, repeatedCode string, err error) {
	if err := ctx.Err(); err != nil {
		return false, 0, "", err
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return true, 0, "", nil
	}
	key, err := g.doomLoopKey(sessionID, tool, args)
	if err != nil {
		return false, 0, "", err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	sess, _ := g.states.Load(sessionID)
	st := sess[key]
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

// Another mutation invalidates this invocation's repetition count.
func (g *MemoryDoomLoopGuard) supersededLocked(sessionID, key string, st *doomLoopState) bool {
	effects, _ := g.effects.Load(sessionID)
	return effects.seq > st.seenEffectSeq && effects.key != key
}

// ResolveRejection clears an invocation's retry history when the host resolves its constraint.
// Cross-argument rejection history remains available for recurring failures.
func (g *MemoryDoomLoopGuard) ResolveRejection(ctx context.Context, sessionID, tool string, args map[string]any, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := g.doomLoopKey(sessionID, tool, args)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sess, _ := g.states.Load(sessionID)
	if st := sess[key]; st != nil && code != "" && st.lastCode == code {
		delete(sess, key)
	}
	return nil
}

// RecordAttempt updates invocation and rejection counts; mutated marks a committed effect.
func (g *MemoryDoomLoopGuard) RecordAttempt(
	ctx context.Context,
	sessionID, responseID, tool string,
	args map[string]any,
	rejectCode string,
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
	key, err := g.doomLoopKey(sessionID, tool, args)
	if err != nil {
		return err
	}
	rejectCode = strings.TrimSpace(rejectCode)
	g.mu.Lock()
	defer g.mu.Unlock()
	sess, ok := g.states.Load(sessionID)
	if !ok || sess == nil {
		sess = make(map[string]*doomLoopState)
	}
	st := sess[key]
	if st == nil {
		st = &doomLoopState{}
		sess[key] = st
	}
	g.states.Store(sessionID, sess)
	g.recordCodeRejectLocked(sessionID, responseID, tool, rejectCode)
	if g.supersededLocked(sessionID, key, st) {
		*st = doomLoopState{}
	}
	effects, _ := g.effects.Load(sessionID)
	st.seenEffectSeq = effects.seq
	if mutated {
		effects.seq++
		effects.key = key
		st.seenEffectSeq = effects.seq
		g.effects.Store(sessionID, effects)
	}
	st.recordResponse(responseID, rejectCode)
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
