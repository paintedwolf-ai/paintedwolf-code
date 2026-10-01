// Package gitstate classifies observed git repository positions and ref
// movements into source-history transitions, from HEAD, the current branch,
// and the reflog's fixed "action: detail" subjects.
package gitstate

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// RepoState is the observed presence of a repository at one root.
type RepoState string

const (
	RepoPresent    RepoState = "repo"
	RepoAbsent     RepoState = "no_repo"
	RepoUnreadable RepoState = "unreadable"
)

// State is one root's observed git position. HeadRef is the short branch name,
// empty when HEAD is detached or no repository is present.
type State struct {
	Repo       RepoState
	HeadCommit string
	HeadRef    string
}

// Equal reports whether two observations describe the same position.
func (s State) Equal(other State) bool {
	return s.Repo == other.Repo && s.HeadCommit == other.HeadCommit && s.HeadRef == other.HeadRef
}

// RefLogEntry is one HEAD reflog row, newest first: the commit HEAD ended at
// and git's action subject (for example "checkout: moving from main to work").
type RefLogEntry struct {
	Commit  string
	Subject string
}

// Transition is one explained movement between observed positions.
type Transition struct {
	Kind       api.SourceGitChangeKind
	FromCommit string
	ToCommit   string
	FromRef    string
	ToRef      string
	Detail     string
}

// MaxTransitionsPerObservation bounds how many reflog entries one observation
// expands into; a longer run collapses to a single summarized transition.
const MaxTransitionsPerObservation = 16

// Classify explains how a root moved from prev to next, returning transitions
// oldest first. The reflog lists HEAD entries newest first. A movement the
// reflog cannot explain yields one unknown transition; an empty result means
// the position did not move.
func Classify(prev, next State, reflog []RefLogEntry) []Transition {
	if prev.Equal(next) {
		return nil
	}
	if lifecycle := classifyLifecycle(prev, next); lifecycle != nil {
		return lifecycle
	}
	if prev.HeadCommit == next.HeadCommit {
		return []Transition{classifyRefOnlyChange(prev, next, reflog)}
	}
	explaining, found := explainingEntries(prev, reflog)
	if !found || len(explaining) == 0 {
		return []Transition{{
			Kind: api.SourceGitChangeUnknown, FromCommit: prev.HeadCommit,
			ToCommit: next.HeadCommit, FromRef: prev.HeadRef, ToRef: next.HeadRef,
		}}
	}
	if len(explaining) > MaxTransitionsPerObservation {
		return []Transition{collapseEntries(prev, next, explaining)}
	}
	return expandEntries(prev, next, explaining)
}

// classifyLifecycle covers repository appearance and disappearance.
func classifyLifecycle(prev, next State) []Transition {
	prevPresent, nextPresent := prev.Repo == RepoPresent, next.Repo == RepoPresent
	switch {
	case !prevPresent && !nextPresent:
		// Absent to unreadable (or back) is not a movement worth narrating.
		return []Transition{}
	case !prevPresent && nextPresent:
		return []Transition{{
			Kind:     api.SourceGitChangeRepoAppeared,
			ToCommit: next.HeadCommit, ToRef: next.HeadRef,
		}}
	case prevPresent && !nextPresent:
		return []Transition{{
			Kind:       api.SourceGitChangeRepoGone,
			FromCommit: prev.HeadCommit, FromRef: prev.HeadRef,
			Detail: string(next.Repo),
		}}
	}
	return nil
}

// classifyRefOnlyChange explains a branch change at an unchanged commit: a
// reflog checkout entry naming the new branch confirms it; anything else
// stays unknown, since a rename writes no HEAD reflog entry.
func classifyRefOnlyChange(prev, next State, reflog []RefLogEntry) Transition {
	out := Transition{
		FromCommit: prev.HeadCommit, ToCommit: next.HeadCommit,
		FromRef: prev.HeadRef, ToRef: next.HeadRef,
		Kind: api.SourceGitChangeUnknown,
	}
	if len(reflog) == 0 {
		return out
	}
	action, detail := parseSubject(reflog[0].Subject)
	if kindForAction(action) == api.SourceGitChangeCheckout && checkoutDestination(detail) == next.HeadRef {
		out.Kind = api.SourceGitChangeCheckout
		out.Detail = detail
	}
	return out
}

// explainingEntries returns the reflog rows newer than prev's position. Each
// row records the commit HEAD ended at, so the row holding prev's commit is
// the movement that created prev and is excluded.
func explainingEntries(prev State, reflog []RefLogEntry) ([]RefLogEntry, bool) {
	if prev.HeadCommit == "" && len(reflog) > 0 {
		action, _ := parseSubject(reflog[len(reflog)-1].Subject)
		if action == "commit (initial)" {
			return reflog, true
		}
	}
	for i, entry := range reflog {
		if entry.Commit == prev.HeadCommit {
			return reflog[:i], true
		}
	}
	// prev was never in the log (expired, truncated, or a foreign clone).
	return nil, false
}

// expandEntries maps explaining rows (newest first) to transitions oldest
// first, chaining from-commits through the log.
func expandEntries(prev, next State, entries []RefLogEntry) []Transition {
	out := make([]Transition, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		action, detail := parseSubject(entries[i].Subject)
		transition := Transition{
			Kind: kindForAction(action), ToCommit: entries[i].Commit, Detail: detail,
		}
		if transition.Kind == api.SourceGitChangeOther {
			transition.Detail = strings.TrimSpace(action + ": " + detail)
		}
		if i == len(entries)-1 {
			transition.FromCommit, transition.FromRef = prev.HeadCommit, prev.HeadRef
		} else {
			transition.FromCommit = entries[i+1].Commit
		}
		if i == 0 {
			transition.ToRef = next.HeadRef
		}
		out = append(out, transition)
	}
	return out
}

// collapseEntries summarizes a run too long to narrate row by row.
func collapseEntries(prev, next State, entries []RefLogEntry) Transition {
	seen := make(map[string]struct{}, 4)
	actions := make([]string, 0, 4)
	for _, entry := range entries {
		action, _ := parseSubject(entry.Subject)
		if _, ok := seen[action]; ok || action == "" {
			continue
		}
		seen[action] = struct{}{}
		actions = append(actions, action)
	}
	return Transition{
		Kind: api.SourceGitChangeOther, FromCommit: prev.HeadCommit,
		ToCommit: next.HeadCommit, FromRef: prev.HeadRef, ToRef: next.HeadRef,
		Detail: fmt.Sprintf("%d operations: %s", len(entries), strings.Join(actions, ", ")),
	}
}

// parseSubject splits git's "action: detail" reflog subject. The parenthetical
// variant ("commit (amend): …") stays part of the action.
func parseSubject(subject string) (action, detail string) {
	action, detail, ok := strings.Cut(subject, ":")
	if !ok {
		return strings.TrimSpace(subject), ""
	}
	return strings.TrimSpace(action), strings.TrimSpace(detail)
}

// kindForAction maps a reflog action to the closed wire vocabulary.
func kindForAction(action string) api.SourceGitChangeKind {
	base, variant := action, ""
	if open := strings.Index(action, "("); open >= 0 {
		base = strings.TrimSpace(action[:open])
		variant = strings.Trim(strings.TrimSpace(action[open:]), "()")
	}
	// "rebase -i (pick)" carries the flag in the base token.
	base, _, _ = strings.Cut(base, " ")
	switch base {
	case "checkout":
		return api.SourceGitChangeCheckout
	case "commit":
		if variant == "amend" {
			return api.SourceGitChangeAmend
		}
		return api.SourceGitChangeCommit
	case "merge":
		return api.SourceGitChangeMerge
	case "rebase":
		return api.SourceGitChangeRebase
	case "pull":
		return api.SourceGitChangePull
	case "reset":
		return api.SourceGitChangeReset
	case "cherry-pick":
		return api.SourceGitChangeCherryPick
	case "revert":
		return api.SourceGitChangeRevert
	case "clone":
		return api.SourceGitChangeClone
	}
	return api.SourceGitChangeOther
}

// checkoutDestination reads the target of git's fixed checkout subject
// ("moving from <a> to <b>"); empty when the subject has another shape.
func checkoutDestination(detail string) string {
	rest, ok := strings.CutPrefix(detail, "moving from ")
	if !ok {
		return ""
	}
	if idx := strings.LastIndex(rest, " to "); idx >= 0 {
		return strings.TrimSpace(rest[idx+len(" to "):])
	}
	return ""
}
