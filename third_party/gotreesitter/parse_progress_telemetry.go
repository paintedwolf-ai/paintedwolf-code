package gotreesitter

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type parseProgressTelemetry struct {
	enabled         bool
	start           time.Time
	nextLoop        time.Time
	nextDetail      time.Time
	interval        time.Duration
	language        string
	sourceBytes     int
	expectedEOFByte uint32
	emittedPhase    map[string]bool
	pendingEndPhase string
}

func newParseProgressTelemetry(p *Parser, sourceBytes int, expectedEOFByte uint32, start time.Time) parseProgressTelemetry {
	if strings.TrimSpace(os.Getenv("GOT_PARSE_PROGRESS")) != "1" {
		return parseProgressTelemetry{}
	}
	interval := time.Second
	if raw := strings.TrimSpace(os.Getenv("GOT_PARSE_PROGRESS_INTERVAL_MS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			interval = time.Duration(n) * time.Millisecond
		}
	}
	language := ""
	if p != nil && p.language != nil {
		language = p.language.Name
	}
	if language == "" {
		language = "unknown"
	}
	return parseProgressTelemetry{
		enabled:         true,
		start:           start,
		nextLoop:        start,
		nextDetail:      start,
		interval:        interval,
		language:        language,
		sourceBytes:     sourceBytes,
		expectedEOFByte: expectedEOFByte,
		emittedPhase:    make(map[string]bool, 8),
	}
}

func (t *parseProgressTelemetry) maybeLoop(now time.Time, iter int, tokens uint64, tok Token, haveToken bool, stacks []glrStack, maxStacksSeen, nodeCount, peakDepth int, needToken bool, singleIterations, multiIterations int) {
	if t == nil || !t.enabled || now.Before(t.nextLoop) {
		return
	}
	t.nextLoop = now.Add(t.interval)
	t.emit(now, "loop", iter, tokens, tok, haveToken, stacks, maxStacksSeen, nodeCount, peakDepth, needToken, singleIterations, multiIterations, "")
}

func (t *parseProgressTelemetry) beginDetail(now time.Time, phase, endPhase string, iter int, tokens uint64, tok Token, haveToken bool, stacks []glrStack, maxStacksSeen, nodeCount, peakDepth int, needToken bool, singleIterations, multiIterations int, extra string) {
	if t == nil || !t.enabled {
		return
	}
	if !t.emittedPhase[phase] || !now.Before(t.nextDetail) {
		t.emittedPhase[phase] = true
		t.pendingEndPhase = endPhase
		t.nextDetail = now.Add(t.interval)
		t.emit(now, phase, iter, tokens, tok, haveToken, stacks, maxStacksSeen, nodeCount, peakDepth, needToken, singleIterations, multiIterations, extra)
	}
}

func (t *parseProgressTelemetry) endDetail(now time.Time, phase string, iter int, tokens uint64, tok Token, haveToken bool, stacks []glrStack, maxStacksSeen, nodeCount, peakDepth int, needToken bool, singleIterations, multiIterations int, extra string) {
	if t == nil || !t.enabled || t.pendingEndPhase != phase {
		return
	}
	t.pendingEndPhase = ""
	t.emittedPhase[phase] = true
	t.emit(now, phase, iter, tokens, tok, haveToken, stacks, maxStacksSeen, nodeCount, peakDepth, needToken, singleIterations, multiIterations, extra)
}

func (t *parseProgressTelemetry) emit(now time.Time, phase string, iter int, tokens uint64, tok Token, haveToken bool, stacks []glrStack, maxStacksSeen, nodeCount, peakDepth int, needToken bool, singleIterations, multiIterations int, extra string) {
	if t == nil || !t.enabled {
		return
	}
	liveStacks := 0
	for i := range stacks {
		if !stacks[i].dead {
			liveStacks++
		}
	}
	fmt.Fprintf(os.Stderr,
		"PARSE-PROGRESS phase=%s lang=%s source_bytes=%d expected_eof=%d elapsed_ms=%d iter=%d tokens=%d stacks=%d live_stacks=%d max_stacks=%d node_count=%d peak_depth=%d need_token=%t single_iters=%d multi_iters=%d",
		phase,
		t.language,
		t.sourceBytes,
		t.expectedEOFByte,
		now.Sub(t.start).Milliseconds(),
		iter,
		tokens,
		len(stacks),
		liveStacks,
		maxStacksSeen,
		nodeCount,
		peakDepth,
		needToken,
		singleIterations,
		multiIterations,
	)
	if haveToken {
		fmt.Fprintf(os.Stderr,
			" token_symbol=%d token_start=%d token_end=%d token_no_lookahead=%t token_eof=%t",
			tok.Symbol,
			tok.StartByte,
			tok.EndByte,
			tok.NoLookahead,
			tok.Symbol == 0 && tok.StartByte == tok.EndByte && !tok.NoLookahead,
		)
	}
	if extra != "" {
		fmt.Fprintf(os.Stderr, " %s", extra)
	}
	fmt.Fprintln(os.Stderr)
}
