// Package textrank provides domain-neutral lexical retrieval: tokenization,
// stemming, fuzzy aliasing, and BM25F field scoring over a corpus.
package textrank

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	// DefaultK1 tunes BM25 term-frequency saturation.
	DefaultK1 = 1.2
	// DefaultB tunes field-length normalization (0 = off, 1 = full).
	DefaultB = 0.5
	// DefaultFuzzyMinLen is the minimum query term length for fuzzy matching.
	DefaultFuzzyMinLen = 4
	// DefaultFuzzyBoost discounts fuzzy matches relative to exact terms.
	DefaultFuzzyBoost = 0.5
	// stemMinLen keeps plural stripping from eroding short tokens to noise.
	stemMinLen = 4
)

// Options configure tokenization and BM25F scoring for one retrieval domain.
type Options struct {
	// SplitIdent splits identifiers on camelCase / PascalCase / digit boundaries
	// and also emits the whole compound (code search); off for natural language.
	SplitIdent bool
	// Stem folds common English plurals so query and document tokens align.
	Stem bool
	// Normalize divides raw BM25F by the query's saturation ceiling
	// (Σ boost·idf·(k1+1)) for a [0,1) score that additive bonuses can sit on.
	Normalize bool
	// K1, B tune BM25F; FuzzyMinLen, FuzzyBoost tune the single-edit alias.
	K1, B                   float64
	FuzzyMinLen, FuzzyBoost float64
}

// CodeOptions tunes source-code retrieval: identifier splitting and plural
// stemming on, standard BM25F, bounded fuzzy matching, raw scores.
func CodeOptions() Options {
	return Options{
		SplitIdent:  true,
		Stem:        true,
		K1:          DefaultK1,
		B:           DefaultB,
		FuzzyMinLen: DefaultFuzzyMinLen,
		FuzzyBoost:  DefaultFuzzyBoost,
	}
}

// Field is one weighted field of a retrieval doc. Field position i means the
// same field in every doc of a scoring call, and its average length feeds normalization.
type Field struct {
	Text   string
	Weight float64
}

// queryTerm is one analyzed query term with a relevance boost (1 for a user
// term, Options.FuzzyBoost for a single-edit alias).
type queryTerm struct {
	term  string
	boost float64
}

// SplitIdent splits an identifier into lowercased camelCase / PascalCase parts,
// treating any non-alphanumeric rune as a boundary.
func SplitIdent(s string) []string {
	if s == "" {
		return nil
	}
	var parts []string
	var b strings.Builder
	runes := []rune(s)
	flush := func() {
		if b.Len() > 0 {
			parts = append(parts, strings.ToLower(b.String()))
			b.Reset()
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		b.WriteRune(r)
	}
	flush()
	return parts
}

// Analyze tokenizes text into lowercased terms with repeats. With splitIdent,
// "createStore" yields "create", "store", and "createstore". With stem, each
// token is plural-folded. 1-rune fragments drop.
func Analyze(s string, splitIdent, stem bool) []string {
	if s == "" {
		return nil
	}
	norm := func(tok string) string {
		if stem {
			return Stem(tok)
		}
		return tok
	}
	var out []string
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !splitIdent {
			if low := strings.ToLower(seg); len(low) > 1 {
				out = append(out, norm(low))
			}
			continue
		}
		parts := SplitIdent(seg)
		for _, p := range parts {
			if len(p) > 1 {
				out = append(out, norm(p))
			}
		}
		// Emit the whole compound only when camel splitting produced more than
		// one part, so a single-word segment is not counted twice.
		if len(parts) > 1 {
			if low := strings.ToLower(seg); len(low) > 1 {
				out = append(out, norm(low))
			}
		}
	}
	return out
}

// AnalyzeQuery returns the deduped, order-stable set of analyzed query terms.
func AnalyzeQuery(query string, splitIdent, stem bool) []string {
	terms := Analyze(query, splitIdent, stem)
	if len(terms) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Stem folds common English plurals (handlers→handler, classes→class,
// libraries→library). Short tokens are kept; verb and derivational forms are not stemmed.
func Stem(t string) string {
	if len(t) < stemMinLen {
		return t
	}
	switch {
	case strings.HasSuffix(t, "ies") && len(t) > 4:
		return t[:len(t)-3] + "y"
	case strings.HasSuffix(t, "es") && len(t) > 3 && esClusterStem(t):
		return t[:len(t)-2]
	case strings.HasSuffix(t, "s") &&
		!strings.HasSuffix(t, "ss") &&
		!strings.HasSuffix(t, "us") &&
		!strings.HasSuffix(t, "is"):
		return t[:len(t)-1]
	}
	return t
}

// esClusterStem reports whether an "es" plural sits on a sibilant cluster
// (classes, boxes, matches, quizzes) where the whole "es" is the plural marker.
func esClusterStem(t string) bool {
	c := t[len(t)-3]
	return c == 's' || c == 'x' || c == 'z' || c == 'h'
}

// Corpus holds BM25F document-frequency and field-length statistics, fit once
// and reused to score docs inside or outside the fitted set against one IDF.
type Corpus struct {
	opt    Options
	n      int
	df     map[string]int
	vocab  map[string]struct{}
	avgLen []float64
}

// Fit computes document frequency, vocabulary, and per-position average field length over docs.
func Fit(docs [][]Field, opt Options) *Corpus {
	c := &Corpus{opt: opt, n: len(docs), df: map[string]int{}, vocab: map[string]struct{}{}}
	var lenSum []float64
	var lenCnt []int
	for _, d := range docs {
		present := map[string]struct{}{}
		for j, f := range d {
			toks := Analyze(f.Text, opt.SplitIdent, opt.Stem)
			for len(lenSum) <= j {
				lenSum = append(lenSum, 0)
				lenCnt = append(lenCnt, 0)
			}
			lenSum[j] += float64(len(toks))
			lenCnt[j]++
			for _, t := range toks {
				c.vocab[t] = struct{}{}
				present[t] = struct{}{}
			}
		}
		for t := range present {
			c.df[t]++
		}
	}
	c.avgLen = make([]float64, len(lenSum))
	for j := range lenSum {
		if lenCnt[j] > 0 {
			c.avgLen[j] = lenSum[j] / float64(lenCnt[j])
		}
		if c.avgLen[j] == 0 {
			c.avgLen[j] = 1
		}
	}
	return c
}

// Unfitted returns a corpus with no statistics: IDF is uniform (1), there is no
// length normalization, and fuzzy aliasing is disabled (no vocabulary).
func Unfitted(opt Options) *Corpus {
	return &Corpus{opt: opt, df: map[string]int{}, vocab: map[string]struct{}{}}
}

func (c *Corpus) idf(term string) float64 {
	if c.n == 0 {
		return 1
	}
	df := float64(c.df[term])
	return math.Log(1 + (float64(c.n)-df+0.5)/(df+0.5))
}

// ScoreAnalyzed scores a doc's weighted fields against pre-analyzed query terms.
// A term missing from the vocabulary scores as its nearest single-edit term at
// Options.FuzzyBoost; an unfitted corpus scores terms at uniform IDF.
func (c *Corpus) ScoreAnalyzed(terms []string, doc []Field) float64 {
	resolved := c.resolve(terms)
	if len(resolved) == 0 {
		return 0
	}
	tf := make([]map[string]int, len(doc))
	length := make([]int, len(doc))
	for j, f := range doc {
		toks := Analyze(f.Text, c.opt.SplitIdent, c.opt.Stem)
		m := make(map[string]int, len(toks))
		for _, t := range toks {
			m[t]++
		}
		tf[j] = m
		length[j] = len(toks)
	}
	raw, ceiling := 0.0, 0.0
	for _, qt := range resolved {
		idf := c.idf(qt.term)
		ceiling += qt.boost * idf * (c.opt.K1 + 1)
		wtf := 0.0
		for j, f := range doc {
			occ := tf[j][qt.term]
			if occ == 0 {
				continue
			}
			normLen := 1.0
			if j < len(c.avgLen) && c.avgLen[j] > 0 {
				normLen = 1 - c.opt.B + c.opt.B*(float64(length[j])/c.avgLen[j])
			}
			wtf += f.Weight * float64(occ) / normLen
		}
		if wtf > 0 {
			raw += qt.boost * idf * (wtf * (c.opt.K1 + 1)) / (wtf + c.opt.K1)
		}
	}
	if c.opt.Normalize {
		if ceiling <= 0 {
			return 0
		}
		return raw / ceiling
	}
	return raw
}

// resolve maps query terms to scorable terms: present in vocab → full weight;
// absent (length ≥ FuzzyMinLen) → nearest single-edit alias at FuzzyBoost, ties
// broken lexicographically. An unfitted corpus keeps every term at full weight.
func (c *Corpus) resolve(terms []string) []queryTerm {
	out := make([]queryTerm, 0, len(terms))
	for _, q := range terms {
		if c.n == 0 {
			out = append(out, queryTerm{term: q, boost: 1})
			continue
		}
		if _, ok := c.vocab[q]; ok {
			out = append(out, queryTerm{term: q, boost: 1})
			continue
		}
		if c.opt.FuzzyMinLen <= 0 || len(q) < int(c.opt.FuzzyMinLen) {
			continue
		}
		alias := ""
		for v := range c.vocab {
			if len(v) < int(c.opt.FuzzyMinLen) || !WithinOneEdit(q, v) {
				continue
			}
			if alias == "" || v < alias {
				alias = v
			}
		}
		if alias != "" {
			out = append(out, queryTerm{term: alias, boost: c.opt.FuzzyBoost})
		}
	}
	return out
}

// FieldScores scores each doc against the query with BM25F, using the doc set
// as its own corpus. An empty query or doc set scores every doc zero.
func FieldScores(query string, docs [][]Field, opt Options) []float64 {
	scores := make([]float64, len(docs))
	terms := AnalyzeQuery(query, opt.SplitIdent, opt.Stem)
	if len(terms) == 0 || len(docs) == 0 {
		return scores
	}
	c := Fit(docs, opt)
	for i, d := range docs {
		scores[i] = c.ScoreAnalyzed(terms, d)
	}
	return scores
}

// WithinOneEdit reports whether a and b differ by at most one insertion,
// deletion, or substitution (Levenshtein ≤ 1) over bytes — sufficient for the
// ASCII tokens code and query text deal in.
func WithinOneEdit(a, b string) bool {
	if a == b {
		return true
	}
	la, lb := len(a), len(b)
	if la > lb {
		a, b, la, lb = b, a, lb, la
	}
	if lb-la > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < la && j < lb {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if la == lb {
			i++
			j++
		} else {
			j++
		}
	}
	return edits+(lb-j) <= 1
}

// StableOrderByScore returns doc indices ordered by descending score, stable for
// equal scores (original order wins), so a zero-signal query is a safe no-op.
func StableOrderByScore(scores []float64) []int {
	order := make([]int, len(scores))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return scores[order[a]] > scores[order[b]]
	})
	return order
}
