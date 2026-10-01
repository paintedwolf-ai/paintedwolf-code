package ignores

import (
	"errors"
	"fmt"
	"strings"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

const ignoreDateLayout = "2006-01-02"

var (
	// ErrIgnoreNoPredicate rejects an entry that would match every finding.
	ErrIgnoreNoPredicate = errors.New("ignore entry names no predicate")
	// ErrIgnoreNoReason rejects an entry with no reason.
	ErrIgnoreNoReason = errors.New("ignore entry needs a reason")
	// ErrIgnoreJustificationScope rejects a VEX justification without an advisory.
	ErrIgnoreJustificationScope = errors.New("ignore justification requires an advisory")
	// ErrIgnoreJustificationInvalid rejects a value outside the OpenVEX vocabulary.
	ErrIgnoreJustificationInvalid = errors.New("ignore justification is not a known value")
	// ErrIgnoreExpiryInvalid rejects an expiry that is not a calendar date.
	ErrIgnoreExpiryInvalid = errors.New("ignore expiry must be a YYYY-MM-DD date")
	// ErrIgnoreKindInvalid rejects a kind outside the finding vocabulary.
	ErrIgnoreKindInvalid = errors.New("ignore kind is not a known finding kind")
	// ErrIgnoreEntryNotFound reports an id the project's ignore file does not hold.
	ErrIgnoreEntryNotFound = errors.New("ignore entry is not in this project's file")
	// ErrIgnoreProjectNotTrusted refuses writes to a project whose scan
	// configuration is untrusted; its ignores would not apply.
	ErrIgnoreProjectNotTrusted = errors.New("this project's scan configuration is not trusted, so its ignores do not apply")
)

// IgnoreEntry is one decision. Predicates are optional and conjoin.
type IgnoreEntry struct {
	decodeError error
	// ID is stable across edits so a client can withdraw exactly this entry.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Path is a repo-relative glob over the finding's primary location. A bare
	// directory covers everything under it.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Kind is the host's finding classification, which every scanner maps onto.
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Scanner narrows an entry to one engine; empty spans all of them.
	Scanner string `yaml:"scanner,omitempty" json:"scanner,omitempty"`
	// Rule is an engine rule id, matched as a glob.
	Rule string `yaml:"rule,omitempty" json:"rule,omitempty"`
	// Advisory matches any published id of the vulnerability, aliases included.
	Advisory string `yaml:"advisory,omitempty" json:"advisory,omitempty"`
	// Fingerprint names exactly one finding.
	Fingerprint string `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"`

	Reason        string `yaml:"reason" json:"reason"`
	Justification string `yaml:"justification,omitempty" json:"justification,omitempty"`
	// Expires is a calendar day, exclusive: the entry stops matching on it.
	Expires string `yaml:"expires,omitempty" json:"expires,omitempty"`
}

// IgnoreSubject supplies the same finding facts to ingest and ledger filtering.
type IgnoreSubject struct {
	Path        string
	Kind        string
	ScannerID   string
	RuleID      string
	AdvisoryIDs []string
	Fingerprint string
}

// IgnoreSubjectFor reads a finding's subject.
func IgnoreSubjectFor(finding api.SecurityFinding) IgnoreSubject {
	subject := IgnoreSubject{
		Path:        scanfindings.PrimaryURI(finding),
		RuleID:      finding.RuleID,
		ScannerID:   finding.Tool.DriverID,
		Fingerprint: finding.Fingerprints.Primary,
	}
	if props := finding.Properties; props != nil && props.Lycaon != nil {
		subject.Kind = string(props.Lycaon.Kind)
		subject.AdvisoryIDs = scanfindings.AdvisoryIDs(props.Lycaon.Advisory)
	}
	return subject
}

// Validate reports why an entry cannot be honoured, or nil.
func (e IgnoreEntry) Validate() error {
	if e.decodeError != nil {
		return e.decodeError
	}
	if !e.hasPredicate() {
		return ErrIgnoreNoPredicate
	}
	if strings.TrimSpace(e.Reason) == "" {
		return ErrIgnoreNoReason
	}
	if kind := strings.TrimSpace(e.Kind); kind != "" && !knownFindingKind(kind) {
		return fmt.Errorf("%w: %s", ErrIgnoreKindInvalid, kind)
	}
	if _, err := e.expiryTime(); err != nil {
		return err
	}
	if j := strings.TrimSpace(e.Justification); j != "" {
		if strings.TrimSpace(e.Advisory) == "" {
			return ErrIgnoreJustificationScope
		}
		if !knownIgnoreJustification(j) {
			return fmt.Errorf("%w: %s", ErrIgnoreJustificationInvalid, j)
		}
	}
	return nil
}

func (e IgnoreEntry) hasPredicate() bool {
	for _, predicate := range []string{e.Path, e.Kind, e.Scanner, e.Rule, e.Advisory, e.Fingerprint} {
		if strings.TrimSpace(predicate) != "" {
			return true
		}
	}
	return false
}

func (e IgnoreEntry) expiryTime() (time.Time, error) {
	raw := strings.TrimSpace(e.Expires)
	if raw == "" {
		return time.Time{}, nil
	}
	day, err := time.Parse(ignoreDateLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s", ErrIgnoreExpiryInvalid, raw)
	}
	return day.UTC(), nil
}

// Expired entries remain in the file for inspection.
func (e IgnoreEntry) Expired(now time.Time) bool {
	day, err := e.expiryTime()
	if err != nil || day.IsZero() {
		return false
	}
	return !now.UTC().Before(day)
}

// Matches ignores expiry so listings can explain lapsed entries.
func (e IgnoreEntry) Matches(subject IgnoreSubject) bool {
	if !e.hasPredicate() {
		return false
	}
	if path := strings.TrimSpace(e.Path); path != "" && !ignorePathMatches(path, subject.Path) {
		return false
	}
	if kind := strings.TrimSpace(e.Kind); kind != "" && !strings.EqualFold(kind, subject.Kind) {
		return false
	}
	if scanner := strings.TrimSpace(e.Scanner); scanner != "" && scanner != strings.TrimSpace(subject.ScannerID) {
		return false
	}
	if rule := strings.TrimSpace(e.Rule); rule != "" && !ignoreRuleMatches(rule, subject.RuleID) {
		return false
	}
	if advisory := strings.TrimSpace(e.Advisory); advisory != "" && !ignoreAdvisoryMatches(advisory, subject.AdvisoryIDs) {
		return false
	}
	if fp := strings.TrimSpace(e.Fingerprint); fp != "" && fp != strings.TrimSpace(subject.Fingerprint) {
		return false
	}
	return true
}

// ignorePathMatches uses pathglob scope rules: a bare directory covers its
// subtree and ** spans segments.
func ignorePathMatches(pattern, path string) bool {
	rel := pathglob.NormalizeRel(path)
	if rel == "" {
		return false
	}
	return pathglob.Covers(pattern, rel)
}

// ignoreRuleMatches globs rule ids, which are flat strings.
func ignoreRuleMatches(pattern, ruleID string) bool {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return false
	}
	if pattern == ruleID {
		return true
	}
	return pathglob.Match(pattern, ruleID)
}

// ignoreAdvisoryMatches compares against every alias of the advisory.
func ignoreAdvisoryMatches(advisory string, ids []string) bool {
	want := strings.ToLower(strings.TrimSpace(advisory))
	for _, id := range ids {
		if strings.ToLower(strings.TrimSpace(id)) == want {
			return true
		}
	}
	return false
}

func knownFindingKind(value string) bool {
	for _, kind := range api.AllFindingKindValues() {
		if strings.EqualFold(string(kind), value) {
			return true
		}
	}
	return false
}

func knownIgnoreJustification(value string) bool {
	for _, known := range api.AllFindingIgnoreJustificationValues() {
		if string(known) == value {
			return true
		}
	}
	return false
}
