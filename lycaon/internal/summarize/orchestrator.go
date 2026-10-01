package summarize

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
)

// Engine errors surfaced to the native tool, which maps them to reject codes.
var (
	// ErrNoMaterial means gather produced zero candidates.
	ErrNoMaterial    = errors.New("summarize: gather produced no material")
	ErrInvalidCursor = errors.New("summarize: cursor is not valid for this scope")
)

// Engine gathers and assembles one briefing pack.
type Engine struct {
	Gather   Gatherer
	Caps     Caps
	Outliner OutlineProvider
	// WireFit estimates the complete response size.
	WireFit WireFitEstimator
	// WireEnvelopeTokens reserves space outside the pack.
	WireEnvelopeTokens int
	// Rerank blends the decision engine into every task-ranked list; the zero
	// value keeps lexical order everywhere.
	Rerank decide.Reranker
	now    func() time.Time // default time.Now; tests inject
}

// NewEngine constructs an orchestration engine.
func NewEngine(gather Gatherer, caps Caps) *Engine {
	return &Engine{Gather: gather, Caps: caps, now: time.Now}
}

// Run executes a validated request end to end on the one-call fill path.
func (e *Engine) Run(ctx context.Context, req Request) (Result, error) {
	if e.Outliner != nil {
		invocation := *e
		invocation.Outliner = &curatorOutliner{provider: e.Outliner, fileLimit: e.Caps.Gather.MaxFilesRead, nameLimit: e.Caps.Pack.SubtreeNameIndexMax, observations: map[string]outlineObservation{}}
		e = &invocation
	}
	req.Task = DefaultTask(req.Task, req.Path, req.Paths, req.Content)
	var cursor cursorState
	if req.Cursor != "" {
		var ok bool
		cursor, ok = decodeCursor(req.Cursor)
		if !ok || cursor.Scope != cursorScope(req) {
			return Result{}, ErrInvalidCursor
		}
		req.CursorPosition = cursor.Position
		req.CursorMatchesObserved = cursor.MatchesObserved
		req.CursorMatchingFilesObserved = cursor.MatchingFilesObserved
	}
	start := e.now()
	gr, err := e.Gather.Gather(ctx, req)
	gatherDone := e.now()
	if err != nil {
		return Result{}, err
	}
	if len(gr.Candidates) == 0 && len(gr.Structure) == 0 &&
		(gr.Subtree == nil || gr.Subtree.Material.SourceFiles == 0) && req.Pattern == "" {
		return Result{}, ErrNoMaterial
	}
	if req.Cursor != "" {
		validPosition := gr.CursorFound
		if strings.TrimSpace(req.Pattern) == "" {
			validPosition = gr.CursorFound || subtreeHasCursor(gr.Subtree, cursor.Position)
		}
		if cursor.Revision != gr.CatalogRevision || !validPosition {
			return Result{}, ErrInvalidCursor
		}
	}
	packBudget := e.initialPackBudget()
	res, err := e.runFill(ctx, req, gr, packBudget)
	if err != nil {
		return Result{}, err
	}
	res = e.fitWire(req, res)
	return e.stampTiming(res, start, gatherDone), nil
}

func subtreeHasCursor(root *SubtreeNode, cursor string) bool {
	if root == nil || cursor == "" {
		return false
	}
	for _, child := range root.Children {
		if child != nil && child.Path == cursor {
			return true
		}
	}
	return false
}

// initialPackBudget reserves envelope headroom.
func (e *Engine) initialPackBudget() int {
	configured := e.Caps.Pack.InputBudgetTokens
	if configured <= 0 {
		configured = DefaultCaps().Pack.InputBudgetTokens
	}
	wireBudget := e.Caps.Pack.WireBudgetTokens
	if e.WireFit == nil || wireBudget <= 0 || e.WireEnvelopeTokens <= 0 {
		return 0
	}
	packBudget := wireBudget - e.WireEnvelopeTokens
	if packBudget > configured {
		packBudget = configured
	}
	if packBudget < wireFitMinPackBudget {
		packBudget = wireFitMinPackBudget
	}
	return packBudget
}

func (e *Engine) stampTiming(res Result, start, gatherDone time.Time) Result {
	end := e.now()
	gatherMs := int(gatherDone.Sub(start) / time.Millisecond)
	totalMs := int(end.Sub(start) / time.Millisecond)
	assembleMs := totalMs - gatherMs
	if assembleMs < 0 {
		assembleMs = 0
	}
	res.Orchestration.GatherMs = gatherMs
	res.Orchestration.AssembleMs = assembleMs
	res.Orchestration.TotalMs = totalMs
	return res
}
