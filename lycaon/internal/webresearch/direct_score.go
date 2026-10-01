package webresearch

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/textrank"
)

const (
	// seedRankBonus rewards pages the seed LLM proposed directly.
	seedRankBonus = 0.25
	// The bonus uses the explicit period, independently of query year tokens.
	periodMatchBonus = 0.15
	// Exact adjacent-token phrases receive a capped bonus.
	phraseBonus    = 0.15
	phraseBonusCap = 0.3
	// Title and heading matches outweigh body matches.
	titleFieldWeight       = 3.0
	headingFieldWeight     = 2.5
	descriptionFieldWeight = 2.0
	bodyFieldWeight        = 1.0
	// Per-host limits preserve result diversity.
	maxHitsPerHost = 3
	// seenPenalty favors fresh pages when relevance scores are similar.
	seenPenalty = 0.15
)

// webScoreOptions normalizes lexical scores for additive ranking bonuses.
// Short metadata fields omit length normalization.
func webScoreOptions() textrank.Options {
	return textrank.Options{
		SplitIdent:  false,
		Stem:        true,
		Normalize:   true,
		K1:          textrank.DefaultK1,
		B:           0,
		FuzzyMinLen: textrank.DefaultFuzzyMinLen,
		FuzzyBoost:  textrank.DefaultFuzzyBoost,
	}
}

// queryScorer combines corpus-weighted lexical relevance with web ranking bonuses.
type queryScorer struct {
	// query is the cleaned request text the engine reads as the task.
	query string
	// rerank blends the decision engine into verified-page order.
	rerank decide.Reranker
	// terms includes year tokens because they can identify the query subject.
	terms []string
	// Each candidate pool filters a fresh copy of basePhrases.
	phrases     []string
	basePhrases []string
	// period is the declared time window; fresh is the recency preference
	// already resolved against it.
	period Period
	fresh  bool
	// All candidates share one clock reading.
	now time.Time
	// Unfitted scoring uses uniform IDF without fuzzy matching.
	corpus *textrank.Corpus
	// URLs already returned to the session receive a penalty in both rank passes.
	seen map[string]struct{}
}

func newQueryScorer(query string, matchPhrases []string, fresh bool, period Period) *queryScorer {
	s := &queryScorer{
		query:  strings.TrimSpace(query),
		period: period,
		fresh:  fresh,
		now:    time.Now(),
		corpus: textrank.Unfitted(webScoreOptions()),
	}
	s.phrases = dedupeLowerPhrases(matchPhrases)
	for _, q := range quotedPhrases(query) {
		s.phrases = dedupeLowerPhrases(append(s.phrases, q))
	}
	if q := strings.TrimSpace(strings.ToLower(query)); len(q) >= 2 {
		s.phrases = dedupeLowerPhrases(append(s.phrases, q))
	}
	// Phrase adjacency runs over the raw query tokens.
	var prevPrimary string
	for _, tok := range queryTokens(query) {
		if len(tok) < 2 {
			prevPrimary = ""
			continue
		}
		if prevPrimary != "" {
			s.phrases = dedupeLowerPhrases(append(s.phrases, prevPrimary+" "+tok))
		}
		prevPrimary = tok
	}
	// Lexical terms are analyzed the same way document text will be, so a
	// hyphenated query ("write-ahead") aligns with split page tokens.
	s.terms = append(s.terms, textrank.AnalyzeQuery(query, false, true)...)
	s.basePhrases = append([]string(nil), s.phrases...)
	return s
}

func dedupeLowerPhrases(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := strings.TrimSpace(strings.ToLower(raw))
		if len(p) < 2 {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// markSeen installs the session's already-returned URL set.
func (s *queryScorer) markSeen(urls map[string]struct{}) {
	s.seen = urls
}

// seenPenaltyFor is the rank penalty for a URL already returned this session.
func (s *queryScorer) seenPenaltyFor(rawURL string) float64 {
	if len(s.seen) == 0 {
		return 0
	}
	if _, ok := s.seen[canonicalURL(rawURL)]; ok {
		return seenPenalty
	}
	return 0
}

// weighTerms fits the ranking corpus to the candidate pool.
// Majority phrases cannot admit a probe on their own.
func (s *queryScorer) weighTerms(pool []indexCandidate) {
	if len(pool) == 0 {
		return
	}
	docs := make([][]textrank.Field, len(pool))
	texts := make([]string, len(pool))
	for i, c := range pool {
		text := candidateText(c)
		texts[i] = text
		docs[i] = []textrank.Field{{Text: text, Weight: 1}}
	}
	s.corpus = textrank.Fit(docs, webScoreOptions())
	s.phrases = keepDiscriminatingPhrases(s.basePhrases, texts)
}

// meaningfulPrimary is the number of primary terms that can lexically match.
func (s *queryScorer) meaningfulPrimary() int {
	return len(s.terms)
}

// primaryMatches counts primary terms present in text.
func (s *queryScorer) primaryMatches(text string) int {
	present := make(map[string]struct{})
	for _, t := range textrank.Analyze(text, false, true) {
		present[t] = struct{}{}
	}
	n := 0
	for _, t := range s.terms {
		if _, ok := present[t]; ok {
			n++
		}
	}
	return n
}

// anyPhraseIn reports whether any match phrase occurs in text.
func (s *queryScorer) anyPhraseIn(text string) bool {
	text = strings.ToLower(text)
	for _, phrase := range s.phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// coverage is the normalized BM25F relevance of text to the query, 0..1 — an
// IDF-weighted coverage of the query terms over a single unweighted field.
func (s *queryScorer) coverage(text string) float64 {
	return s.corpus.ScoreAnalyzed(s.terms, []textrank.Field{{Text: text, Weight: 1}})
}

// rankCandidate scores index metadata for the crawl-phase ranking: weighted
// coverage plus provenance, recency, and period-match bonuses.
func (s *queryScorer) rankCandidate(c indexCandidate) float64 {
	text := candidateText(c)
	score := s.coverage(text)
	if s.anyPhraseIn(text) {
		score += phraseBonus
	}
	if c.Source == "llm_seed" || c.Source == "llm_lead" || c.Source == sourceDeclaredPage || c.Source == sourceProviderSeed {
		score += seedRankBonus
	}
	score += s.recencyBoost(c.Date)
	if s.periodMatch(c.Date) {
		score += periodMatchBonus
	}
	return score - s.seenPenaltyFor(c.URL)
}

// probeEligible requires lexical evidence before spending a verification fetch.
// Empty-index seed roots are admitted for thin-page annotation.
func (s *queryScorer) probeEligible(c indexCandidate) bool {
	text := candidateText(c)
	matches := s.primaryMatches(text)
	if matches >= probePrimaryFloor(s.meaningfulPrimary()) {
		return true
	}
	if (c.Source == "llm_seed" || c.Source == "llm_lead") && isSiteRootCandidateURL(c.URL) {
		return true
	}
	return false
}

func isSiteRootCandidateURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	path := strings.Trim(u.Path, "/")
	return path == ""
}

// probePrimaryFloor requires more primary matches for longer queries.
func probePrimaryFloor(primary int) int {
	if primary <= 3 {
		return 1
	}
	if primary >= 8 {
		return 3
	}
	return 2
}

// periodMatch reports a page dated inside the caller's declared window.
func (s *queryScorer) periodMatch(date time.Time) bool {
	if date.IsZero() {
		return false
	}
	return s.period.Includes(date.Year())
}

// scoreProbe combines field-weighted lexical relevance with a capped exact-phrase bonus.
func (s *queryScorer) scoreProbe(probe pageProbe) float64 {
	score := s.corpus.ScoreAnalyzed(s.terms, []textrank.Field{
		{Text: probe.title, Weight: titleFieldWeight},
		{Text: strings.Join(probe.headings, " "), Weight: headingFieldWeight},
		{Text: probe.description, Weight: descriptionFieldWeight},
		{Text: probe.textSample, Weight: bodyFieldWeight},
	})
	fieldTexts := []string{
		strings.ToLower(probe.title),
		strings.ToLower(strings.Join(probe.headings, " ")),
		strings.ToLower(probe.description),
		strings.ToLower(probe.textSample),
	}
	bonus := 0.0
	for _, phrase := range s.phrases {
		for _, ft := range fieldTexts {
			if strings.Contains(ft, phrase) {
				bonus += phraseBonus
				break
			}
		}
		if bonus >= phraseBonusCap {
			bonus = phraseBonusCap
			break
		}
	}
	return score + bonus
}

// minPhraseFilterCorpus is the smallest corpus a frequency judgment can be
// made on; below it every phrase is kept.
const minPhraseFilterCorpus = 8

// keepDiscriminatingPhrases drops phrases found in more than half the candidate texts.
func keepDiscriminatingPhrases(phrases []string, texts []string) []string {
	if len(texts) < minPhraseFilterCorpus || len(phrases) == 0 {
		return phrases
	}
	half := len(texts) / 2
	out := make([]string, 0, len(phrases))
	for _, phrase := range phrases {
		df := 0
		for _, text := range texts {
			if strings.Contains(text, phrase) {
				df++
				if df > half {
					break
				}
			}
		}
		if df <= half {
			out = append(out, phrase)
		}
	}
	return out
}

// minSnippetSentenceLen skips tagline fragments ("Fast.", "Reliable.") when a
// fuller matching sentence exists.
const minSnippetSentenceLen = 30

// bestSentence extracts the sentence from a text sample that best matches the
// query — the snippet fallback when a page has no meta description.
func (s *queryScorer) bestSentence(sample string) string {
	sample = strings.TrimSpace(sample)
	if sample == "" {
		return ""
	}
	best, bestScore := "", 0.0
	short, shortScore := "", 0.0
	for _, sentence := range splitSentences(sample) {
		sc := s.coverage(strings.ToLower(sentence))
		if sc <= 0 {
			continue
		}
		if len(sentence) >= minSnippetSentenceLen {
			if sc > bestScore {
				best, bestScore = sentence, sc
			}
		} else if sc > shortScore {
			short, shortScore = sentence, sc
		}
	}
	if best != "" {
		return best
	}
	return short
}

func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '.', '!', '?':
			if sentence := strings.TrimSpace(text[start : i+1]); sentence != "" {
				out = append(out, sentence)
			}
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func candidateText(c indexCandidate) string {
	return strings.ToLower(c.Title + " " + c.Notes + " " + c.APISnippet + " " + c.URL)
}

// rankCandidates orders the pool by rankCandidate score; ties break on date
// recency, then URL for determinism.
func rankCandidates(candidates []indexCandidate, scorer *queryScorer) []indexCandidate {
	scored := append([]indexCandidate(nil), candidates...)
	for i := range scored {
		scored[i].Score = scorer.rankCandidate(scored[i])
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if !scored[i].Date.Equal(scored[j].Date) {
			return scored[i].Date.After(scored[j].Date)
		}
		return scored[i].URL < scored[j].URL
	})
	return scored
}

// Fresh queries double recency weight; past periods disable it.
func (s *queryScorer) recencyBoost(date time.Time) float64 {
	if date.IsZero() || s.period.Historical(s.now) {
		return 0
	}
	age := s.now.Sub(date)
	boost := 0.0
	switch {
	case age < 90*24*time.Hour:
		boost = 0.2
	case age < 365*24*time.Hour:
		boost = 0.1
	}
	if s.fresh {
		boost *= 2
	}
	return boost
}

func queryTokens(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ".,;:!?\"'()[]{}")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
