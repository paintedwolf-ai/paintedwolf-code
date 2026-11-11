// Package stepbriefing explains Walk steps: why a change was made and what
// it changed, from durable source and transcript records.
package stepbriefing

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// ProfileID names Walk step explanations among briefing profiles.
const ProfileID = "source_step"

// maxOutsideMembers bounds the effects one outside group may name.
const maxOutsideMembers = 256

type step struct {
	key     string
	ordinal int64
	target  wire.SourceStepBriefingTarget
	effect  *wire.SourceWalkEffect
}

// Targets maps each step on a Walk page to its explanation target, keyed as
// the rail keys it: Git movements before command windows before single
// effects, with consecutive outside-only effects grouped.
func Targets(page wire.SourceWalkResponse) map[string]wire.SourceStepBriefingTarget {
	knownGit := map[string]wire.SourceGitChange{}
	for _, change := range page.GitChanges {
		if _, seen := knownGit[change.ID]; !seen {
			knownGit[change.ID] = change
		}
	}
	knownCommands := map[string]wire.SourceCommandWindow{}
	for _, command := range page.Commands {
		if _, seen := knownCommands[command.ID]; !seen {
			knownCommands[command.ID] = command
		}
	}
	byGit, byCommand := map[string][]wire.SourceWalkEffect{}, map[string][]wire.SourceWalkEffect{}
	var steps []step
	for _, effect := range pageEffects(page) {
		if id := trimmed(effect.GitChangeID); id != "" {
			if _, ok := knownGit[id]; ok {
				byGit[id] = append(byGit[id], effect)
				continue
			}
		}
		if id := trimmed(effect.CommandID); id != "" {
			if _, ok := knownCommands[id]; ok {
				byCommand[id] = append(byCommand[id], effect)
				continue
			}
		}
		effect := effect
		steps = append(steps, step{key: effect.ID, ordinal: effect.Ordinal, effect: &effect, target: wire.SourceStepBriefingTarget{
			Kind: "effect", TargetKey: effect.ID, ScopeFingerprint: fingerprint(effect.ID, effect.AfterVersionID),
			Scope: wire.SourceStepBriefingScope{Kind: "file", RootIds: []string{effect.RootID}, FileID: effect.FileID},
		}})
	}
	for id, change := range knownGit {
		key := "git:" + id
		effects := byGit[id]
		steps = append(steps, step{key: key, ordinal: change.Ordinal, target: groupTarget("git", key, append([]string{change.RootID}, roots(effects)...), effects)})
	}
	for id, command := range knownCommands {
		effects, ok := byCommand[id]
		if !ok {
			continue
		}
		key := "command:" + id
		steps = append(steps, step{key: key, ordinal: command.Ordinal, target: groupTarget("command", key, roots(effects), effects)})
	}
	slices.SortFunc(steps, func(a, b step) int {
		if a.ordinal != b.ordinal {
			if a.ordinal < b.ordinal {
				return -1
			}
			return 1
		}
		return strings.Compare(a.key, b.key)
	})
	out := map[string]wire.SourceStepBriefingTarget{}
	for _, s := range collapseOutside(steps) {
		out[s.key] = s.target
	}
	return out
}

func pageEffects(page wire.SourceWalkResponse) []wire.SourceWalkEffect {
	var effects []wire.SourceWalkEffect
	for _, file := range page.Files {
		effects = append(effects, file.Effects...)
	}
	slices.SortFunc(effects, func(a, b wire.SourceWalkEffect) int {
		if a.Ordinal != b.Ordinal {
			if a.Ordinal < b.Ordinal {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return effects
}

func groupTarget(kind, key string, rootIDs []string, effects []wire.SourceWalkEffect) wire.SourceStepBriefingTarget {
	ids := effectIDs(effects)
	return wire.SourceStepBriefingTarget{Kind: kind, TargetKey: key, ScopeFingerprint: fingerprint(append([]string{key}, ids...)...),
		Scope: wire.SourceStepBriefingScope{Kind: kind, RootIds: unique(rootIDs)}}
}

// collapseOutside groups consecutive effects nobody in a session made; a
// single one stays its own step, and a run over the member bound has no
// target.
func collapseOutside(steps []step) []step {
	var out, run []step
	flush := func() {
		switch {
		case len(run) == 1:
			out = append(out, run[0])
		case len(run) > 1 && len(run) <= maxOutsideMembers:
			effects := make([]wire.SourceWalkEffect, len(run))
			for i, s := range run {
				effects[i] = *s.effect
			}
			key := "outside:" + run[0].effect.ID
			target := groupTarget("outside", key, roots(effects), effects)
			target.EffectIds = effectIDs(effects)
			out = append(out, step{key: key, ordinal: run[0].ordinal, target: target})
		}
		run = nil
	}
	for _, s := range steps {
		if s.effect != nil && outsideOnly(*s.effect) {
			run = append(run, s)
			continue
		}
		flush()
		out = append(out, s)
	}
	flush()
	return out
}

func outsideOnly(effect wire.SourceWalkEffect) bool {
	for _, author := range effect.Contributors {
		if author.Origin != "external" || author.SessionID != "" {
			return false
		}
	}
	return true
}

func effectIDs(effects []wire.SourceWalkEffect) []string {
	ids := make([]string, len(effects))
	for i, effect := range effects {
		ids[i] = effect.ID
	}
	return ids
}

func roots(effects []wire.SourceWalkEffect) []string {
	out := make([]string, 0, len(effects))
	for _, effect := range effects {
		out = append(out, effect.RootID)
	}
	return out
}

func unique(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
