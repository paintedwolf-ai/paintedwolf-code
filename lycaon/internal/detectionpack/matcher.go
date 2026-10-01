package detectionpack

import (
	"log/slog"
	"sync"
)

// Matcher holds an immutable snapshot of the catalog for matching.
type Matcher struct {
	entries []matcherEntry
	// credentialStoreRules supplies write-deny paths at every severity.
	credentialStoreRules []Rule
	skipped              sync.Map // rule id → struct{} — panic once, skip thereafter
}

type matcherEntry struct {
	packID string
	rule   Rule
	order  int // rule file order within its pack (stable tie-break)
}

// NewMatcher builds a matcher over the catalog snapshot. Nil catalog is fine.
func NewMatcher(cat *Catalog) *Matcher {
	m := &Matcher{}
	if cat == nil {
		return m
	}
	for _, p := range cat.Packs {
		if !p.Enabled {
			continue
		}
		for i, r := range p.Rules {
			if !r.Supported {
				continue
			}
			if p.ID == CredentialStorePackID {
				m.credentialStoreRules = append(m.credentialStoreRules, r)
			}
			// Minting rules contribute provenance at every severity.
			switch r.Level {
			case LevelMedium, LevelHigh, LevelCritical:
			default:
				if !EffectFromTags(r.Tags).MintsCredential {
					continue
				}
			}
			m.entries = append(m.entries, matcherEntry{
				packID: p.ID,
				rule:   r,
				order:  i,
			})
		}
	}
	return m
}

// Match uses the device-enabled catalog for tool-execution rules.
func (m *Matcher) Match(ev Event) (Match, bool) {
	return m.match(SourceToolExec, func(r Rule) bool { return r.Matches(ev) })
}

// Mediated egress has no project directory and uses device rules.
func (m *Matcher) MatchEgress(ev EgressEvent) (Match, bool) {
	return m.match(SourceEgressObserved, func(r Rule) bool { return r.MatchesEgress(ev) })
}

// MatchMint finds credential issuance independently of higher-severity matches.
func (m *Matcher) MatchMint(ev Event) (Match, bool) {
	if m == nil {
		return Match{}, false
	}
	for _, e := range m.entries {
		if e.rule.Source != SourceToolExec {
			continue
		}
		if _, skip := m.skipped.Load(e.rule.ID); skip {
			continue
		}
		effect := EffectFromTags(e.rule.Tags)
		if !effect.MintsCredential || !m.safeMatch(e, func(r Rule) bool { return r.Matches(ev) }) {
			continue
		}
		return Match{
			PackID: e.packID, RuleID: e.rule.ID, RuleTitle: e.rule.Title,
			Level: e.rule.Level, MintsCredential: true, Tagged: effect.Tagged,
		}, true
	}
	return Match{}, false
}

// A rule panic disables matching and records a process-lifetime warning.
func (m *Matcher) safeMatch(e matcherEntry, eval func(Rule) bool) (matched bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("detectionpack: rule panicked; skipping",
				"rule_id", e.rule.ID, "pack_id", e.packID, "recover", rec)
			m.skipped.Store(e.rule.ID, struct{}{})
			recordSkippedRulePanic(e.packID, e.rule.ID, rec)
			matched = false
		}
	}()
	return eval(e.rule)
}

func (m *Matcher) match(src LogSource, eval func(Rule) bool) (hit Match, ok bool) {
	if m == nil {
		return Match{}, false
	}
	bestRank := -1
	var best matcherEntry
	found := false
	for _, e := range m.entries {
		if e.rule.Source != src {
			continue
		}
		if _, skip := m.skipped.Load(e.rule.ID); skip {
			continue
		}
		if !m.safeMatch(e, eval) {
			continue
		}
		rank := levelRank(e.rule.Level)
		if !found || rank > bestRank || (rank == bestRank && betterTie(e, best)) {
			bestRank = rank
			best = e
			found = true
		}
	}
	if !found {
		return Match{}, false
	}
	effect := EffectFromTags(best.rule.Tags)
	return Match{
		PackID:        best.packID,
		RuleID:        best.rule.ID,
		RuleTitle:     best.rule.Title,
		Level:         best.rule.Level,
		External:      effect.External,
		Local:         effect.Local,
		Unrecoverable: effect.Unrecoverable,

		MintsCredential: effect.MintsCredential,
		Tagged:          effect.Tagged,
	}, true
}

func levelRank(l Level) int {
	switch l {
	case LevelCritical:
		return 3
	case LevelHigh:
		return 2
	case LevelMedium:
		return 1
	default:
		return 0
	}
}

// Equal-severity matches prefer lower pack IDs, then earlier file order.
func betterTie(a, b matcherEntry) bool {
	if a.packID != b.packID {
		return a.packID < b.packID
	}
	return a.order < b.order
}
