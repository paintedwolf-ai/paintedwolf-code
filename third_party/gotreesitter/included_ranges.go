package gotreesitter

import "sort"

type includedRangeTokenSource struct {
	base   TokenSource
	ranges []Range
	idx    int
}

type includedRangeAwareTokenSource interface {
	setIncludedRanges([]Range) bool
}

func newIncludedRangeTokenSource(base TokenSource, ranges []Range) TokenSource {
	if base == nil || len(ranges) == 0 {
		return base
	}
	ranges = normalizeIncludedRanges(ranges)
	if len(ranges) == 0 {
		return base
	}
	return &includedRangeTokenSource{
		base:   base,
		ranges: ranges,
	}
}

func normalizeIncludedRanges(ranges []Range) []Range {
	if len(ranges) == 0 {
		return nil
	}

	tmp := make([]Range, 0, len(ranges))
	for _, r := range ranges {
		if r.EndByte <= r.StartByte {
			continue
		}
		tmp = append(tmp, r)
	}
	if len(tmp) == 0 {
		return nil
	}

	sort.Slice(tmp, func(i, j int) bool {
		if tmp[i].StartByte != tmp[j].StartByte {
			return tmp[i].StartByte < tmp[j].StartByte
		}
		return tmp[i].EndByte < tmp[j].EndByte
	})

	out := make([]Range, 0, len(tmp))
	cur := tmp[0]
	for i := 1; i < len(tmp); i++ {
		r := tmp[i]
		if r.StartByte <= cur.EndByte {
			if r.EndByte > cur.EndByte {
				cur.EndByte = r.EndByte
				cur.EndPoint = r.EndPoint
			}
			continue
		}
		out = append(out, cur)
		cur = r
	}
	out = append(out, cur)
	return out
}

func (s *includedRangeTokenSource) SetParserState(state StateID) {
	if p, ok := s.base.(parserStateTokenSource); ok {
		p.SetParserState(state)
	}
}

func (s *includedRangeTokenSource) SetGLRStates(states []StateID) {
	if p, ok := s.base.(parserStateTokenSource); ok {
		p.SetGLRStates(states)
	}
}

// lexesErrorModeAtErrorState forwards to the base source's answer. The C
// recovery port (parser_recover_c.go) uses this to decide whether it may
// safely trust a SetParserState(0) token's identity as C-equivalent
// error-mode lookahead, or must substitute its own raw-source Lexer. An
// included-range wrapper has no lexing of its own — it only filters the
// base's tokens against the active ranges — so it must defer entirely to the
// base: answering true unconditionally (or synthesizing lexing) would let the
// C-recovery engine-side error-mode substitution run its Lexer over the
// WHOLE underlying document, ignoring the active ranges, which C never does.
func (s *includedRangeTokenSource) lexesErrorModeAtErrorState() bool {
	if s == nil || s.base == nil {
		return false
	}
	em, ok := s.base.(errorModeLexingTokenSource)
	return ok && em.lexesErrorModeAtErrorState()
}

func (s *includedRangeTokenSource) SupportsIncrementalReuse() bool {
	if s == nil || s.base == nil {
		return false
	}
	return tokenSourceSupportsIncrementalReuse(s.base)
}

func (s *includedRangeTokenSource) IncrementalReuseUnsupportedReason() string {
	if s == nil || s.base == nil {
		return "token_source_nil"
	}
	return incrementalReuseUnavailableReason(s.base)
}

func (s *includedRangeTokenSource) Reset(source []byte) {
	if s == nil {
		return
	}
	s.idx = 0
	if resettable, ok := s.base.(interface{ Reset([]byte) }); ok {
		resettable.Reset(source)
	}
}

func (s *includedRangeTokenSource) Close() {
	if s == nil || s.base == nil {
		return
	}
	if closer, ok := s.base.(interface{ Close() }); ok {
		closer.Close()
	}
}

func (s *includedRangeTokenSource) Next() Token {
	return s.filterToken(Token{}, false)
}

func (s *includedRangeTokenSource) SkipToByte(offset uint32) Token {
	if skipper, ok := s.base.(ByteSkippableTokenSource); ok {
		return s.filterToken(skipper.SkipToByte(offset), true)
	}
	for {
		tok := s.Next()
		if tok.Symbol == 0 || tok.StartByte >= offset {
			return tok
		}
	}
}

func (s *includedRangeTokenSource) SkipToByteWithPoint(offset uint32, pt Point) Token {
	if skipper, ok := s.base.(PointSkippableTokenSource); ok {
		return s.filterToken(skipper.SkipToByteWithPoint(offset, pt), true)
	}
	return s.SkipToByte(offset)
}

func (s *includedRangeTokenSource) RelexFromTokenStart(tok Token) (Token, bool) {
	relexer, ok := s.base.(tokenSourceRelexer)
	if !ok {
		return Token{}, false
	}
	idx := s.idx
	if !s.tokenInCurrentRange(tok) {
		s.idx = idx
		return Token{}, false
	}
	next, ok := relexer.RelexFromTokenStart(tok)
	if !ok {
		return Token{}, false
	}
	if next.StartByte != tok.StartByte || next.StartPoint != tok.StartPoint || !s.tokenInCurrentRange(next) {
		s.idx = idx
		return Token{}, false
	}
	return next, true
}

func (s *includedRangeTokenSource) CanRelexFromTokenStart(tok Token) bool {
	relexer, ok := s.base.(tokenSourceRelexer)
	return ok && relexer.CanRelexFromTokenStart(tok)
}

func (s *includedRangeTokenSource) filterToken(tok Token, hasToken bool) Token {
	for {
		if !hasToken {
			tok = s.base.Next()
		}
		hasToken = false

		if tok.Symbol == 0 {
			return tok
		}
		if !s.advanceToMatchingRange(tok) {
			return Token{
				StartByte:  tok.EndByte,
				EndByte:    tok.EndByte,
				StartPoint: tok.EndPoint,
				EndPoint:   tok.EndPoint,
			}
		}

		r := s.ranges[s.idx]
		if tok.StartByte < r.StartByte && tok.EndByte <= r.StartByte {
			if skipper, ok := s.base.(ByteSkippableTokenSource); ok {
				tok = skipper.SkipToByte(r.StartByte)
				hasToken = true
			}
			continue
		}
		if tok.StartByte < r.StartByte {
			// Re-seek a token that overlaps the selected boundary. A source
			// with byte seeking can reproduce the token from that boundary.
			if skipper, ok := s.base.(ByteSkippableTokenSource); ok {
				tok = skipper.SkipToByte(r.StartByte)
				hasToken = true
				continue
			}
			// A source without byte seeking cannot reproduce a trimmed token.
			// Preserve the complete overlap, which is the conservative existing
			// behavior. Its start can remain before the selected boundary.
			return tok
		}
		if tok.EndByte <= r.StartByte {
			if skipper, ok := s.base.(ByteSkippableTokenSource); ok {
				tok = skipper.SkipToByte(r.StartByte)
				hasToken = true
			}
			continue
		}
		if tok.StartByte >= r.EndByte {
			s.idx++
			hasToken = true
			continue
		}
		return tok
	}
}

func (s *includedRangeTokenSource) tokenInCurrentRange(tok Token) bool {
	if tok.Symbol == 0 || s.idx >= len(s.ranges) {
		return false
	}
	r := s.ranges[s.idx]
	return tok.StartByte < r.EndByte && tok.EndByte > r.StartByte
}

func (s *includedRangeTokenSource) advanceToMatchingRange(tok Token) bool {
	for s.idx < len(s.ranges) && tok.StartByte >= s.ranges[s.idx].EndByte {
		s.idx++
	}
	return s.idx < len(s.ranges)
}
