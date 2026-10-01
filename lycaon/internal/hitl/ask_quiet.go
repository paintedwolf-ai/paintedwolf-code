package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// QuietRecordID is the opaque quiet_ id for a chat/key/ttl triple.
func QuietRecordID(chatSessionID, key string, ttlSeconds int) string {
	if ttlSeconds < 0 {
		ttlSeconds = 0
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(chatSessionID),
		strings.TrimSpace(key),
		"ttl",
		strconv.Itoa(ttlSeconds),
	}, "\x00")))
	return "quiet_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// AskQuiet suppresses later asks of one key. It does not satisfy a gate.
type AskQuiet struct {
	ElevatedEffects  []api.ElevatedAccessEffect `json:"elevated_effects,omitempty"`
	ID               string                     `json:"id"`
	OwnerOperationID string                     `json:"-"`
	ChatSessionID    string                     `json:"chat_session_id"`
	Key              string                     `json:"key"`
	Label            string                     `json:"label"`
	ExpiresAt        *time.Time                 `json:"expires_at,omitempty"`
	Suppressed       int                        `json:"suppressed"`
	CreatedAt        time.Time                  `json:"created_at"`
	SessionTitle     string                     `json:"session_title,omitempty"`
}

// QuietSubject is one quietable reason segment with its store key and label.
type QuietSubject struct {
	ElevatedEffects []api.ElevatedAccessEffect
	Segment         string
	Key             string
	Label           string
}

// QuietSubjectsFromDecision scopes quiets to gate subjects.
// Secret segments include seam and host; carve-outs include the exact action digest.
func QuietSubjectsFromDecision(d *gate.Decision, secret *SecretScreen, actionDigest string) []QuietSubject {
	if d == nil || !d.OffersQuiet() || strings.TrimSpace(actionDigest) == "" {
		return nil
	}
	segments := d.ReasonSegments()
	out := make([]QuietSubject, 0, len(segments))
	for _, segment := range segments {
		gateName, _, ok := strings.Cut(segment, ":")
		if !ok {
			continue
		}
		g := api.ApprovalGate(gateName)
		subj := QuietSubject{ElevatedEffects: elevatedQuietEffects(segment), Segment: segment, Key: segment, Label: quietLabel(segment, secret)}
		switch {
		case gate.ExactActionQuiet(g) || executionQuietSegment(segment):
			subj.Key = segment + "\x00action:" + strings.TrimSpace(actionDigest)
			subj.Label = QuietLabelThisExactAction
		case g == api.GateSecretOutbound && secret != nil:
			subj.Key = secretQuietKey(segment, secret)
			subj.Label = secretQuietLabel(secret)
		}
		out = append(out, subj)
	}
	return out
}

func secretQuietKey(segment string, secret *SecretScreen) string {
	seam := strings.TrimSpace(secret.Surface)
	host := strings.TrimSpace(secret.DestinationLabel)
	if host == "" {
		host = strings.TrimSpace(secret.DestinationID)
	}
	return strings.Join([]string{segment, "seam:" + seam, "host:" + host}, "\x00")
}

// quietLabel is the coverage noun for one quiet reason segment.
func quietLabel(segment string, secret *SecretScreen) string {
	gateName, rest, ok := strings.Cut(segment, ":")
	if !ok {
		return segment
	}
	switch api.ApprovalGate(gateName) {
	case api.GateAuthorityMisuse:
		return strings.ReplaceAll(rest, "/", " / ")
	case api.GateOutsideRootsWrite, api.GateOutsideRootsRead:
		mode, dir, split := strings.Cut(rest, ":")
		if !split {
			return rest
		}
		return mode + "s under " + dir
	case api.GateSensitiveLocation:
		return strings.ReplaceAll(rest, "-", " ")
	case api.GateUnobservedChannel:
		switch {
		case rest == "process_control":
			return "this exact command with process control"
		case rest == "host_execution":
			return "this exact command outside the sandbox"
		case rest == "direct_ip":
			return "direct network access"
		case rest == "unconfined":
			return "commands that run unconfined"
		case strings.HasPrefix(rest, "socket:"):
			return "this local service"
		}
		return rest
	case api.GateUserRule:
		category, pattern, split := strings.Cut(rest, ":")
		if !split {
			return rest
		}
		return "your " + category + " rule for " + pattern
	case api.GateSecretOutbound:
		if secret != nil {
			return secretQuietLabel(secret)
		}
	default:
	}
	if secret != nil {
		return secretQuietLabel(secret)
	}
	return rest
}

func secretQuietLabel(secret *SecretScreen) string {
	rule := firstNonEmpty(secret.RuleTitle, secret.RuleID)
	host := firstNonEmpty(secret.DestinationLabel, secret.DestinationID)
	seam := firstNonEmpty(secret.SurfaceLabel, secret.Surface)
	switch {
	case host != "" && seam != "":
		return rule + " → " + host + " over " + seam
	case host != "":
		return rule + " → " + host
	case seam != "":
		return rule + " over " + seam
	default:
		return rule
	}
}

// DecisionFullyQuieted reports whether every firing gate has a covered quiet
// subject. A gate with no reason segment keeps the card.
func DecisionFullyQuieted(d *gate.Decision, secret *SecretScreen, actionDigest string, covers func(key string) bool) bool {
	if d == nil || covers == nil {
		return false
	}
	subjects := QuietSubjectsFromDecision(d, secret, actionDigest)
	if len(subjects) == 0 {
		return false
	}
	represented := make(map[api.ApprovalGate]struct{}, len(subjects))
	for _, subj := range subjects {
		if !covers(subj.Key) {
			return false
		}
		gateName, _, _ := strings.Cut(subj.Segment, ":")
		represented[api.ApprovalGate(gateName)] = struct{}{}
	}
	for _, g := range d.Gates() {
		if _, ok := represented[g]; !ok {
			return false
		}
	}
	return true
}

func executionQuietSegment(segment string) bool {
	prefix := string(api.GateUnobservedChannel) + ":"
	return segment == prefix+"process_control" || segment == prefix+"host_execution" || strings.HasPrefix(segment, prefix+"native_process:")
}
