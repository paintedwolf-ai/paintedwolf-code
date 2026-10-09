package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
)

// QuietOptions suppresses uncovered subjects for this chat; the plan attaches matching authority.
func QuietOptions(action ProposedAction, decision *gate.Decision, secret *SecretScreen, alreadyQuiet func(key string) bool) []ApprovalOption {
	if decision == nil {
		return nil
	}
	chat := action.Scope.ChatSession()
	if chat == "" {
		return nil
	}
	var subjects []QuietSubject
	for _, subj := range QuietSubjectsFromDecision(decision, secret, GrantKey(action)) {
		if alreadyQuiet != nil && alreadyQuiet(subj.Key) {
			continue
		}
		subjects = append(subjects, subj)
	}
	if len(subjects) == 0 {
		return nil
	}
	return []ApprovalOption{quietOption(chat, subjects)}
}

func quietOption(chat string, subjects []QuietSubject) ApprovalOption {
	labels := make([]string, 0, len(subjects))
	authority := make([]ApprovalAuthorityDelta, 0, len(subjects))
	keyParts := make([]string, 0, len(subjects))
	for _, subj := range subjects {
		labels = append(labels, subj.Label)
		keyParts = append(keyParts, subj.Key)
		authority = append(authority, ApprovalAuthorityDelta{
			Kind:          AuthorityAskQuiet,
			ChatSessionID: chat,
			AskQuiet:      &AskQuietDelta{ElevatedEffects: subj.ElevatedEffects, ID: QuietRecordID(chat, subj.Key, 0), Key: subj.Key, Label: subj.Label},
		})
	}
	return ApprovalOption{
		ID: quietOptionID(chat, keyParts, ApprovalRungChat), Kind: ApprovalOptionQuiet, Rung: ApprovalRungChat, Group: GroupQuiet,
		Title: TitleQuietForThisChat, Coverage: QuietSubjectSummary(labels), ExpiresWhen: ExpiresWhenChatDeletedOrRevoked,
		ReaskWhen:      ReaskWhenQuietRevoked,
		DecisionAction: ApprovalOptionApprove,
		Authority:      authority,
	}
}

func quietOptionID(chat string, keys []string, rung ApprovalOptionRung) string {
	sum := sha256.Sum256([]byte(chat + "\x00" + strings.Join(keys, "\x00") + "\x00" + string(rung)))
	return "quiet_opt_" + base64.RawURLEncoding.EncodeToString(sum[:8])
}

// attachQuietGrantAuthority drops quiet options without matching, bounded authority,
// or when an enabled chat lease already covers the chat.
func attachQuietGrantAuthority(options []ApprovalOption) []ApprovalOption {
	var primary []ApprovalOption
	hasChatLease := false
	for _, o := range options {
		if o.Kind != ApprovalOptionQuiet {
			primary = append(primary, o)
			if o.Rung == ApprovalRungChat && !o.Disabled {
				hasChatLease = true
			}
		}
	}
	if hasChatLease {
		return primary
	}
	if len(primary) == 0 {
		return options
	}
	out := make([]ApprovalOption, 0, len(options))
	for _, o := range options {
		if o.Kind != ApprovalOptionQuiet {
			out = append(out, o)
			continue
		}
		src := quietGrantSource(primary)
		if src == nil || len(src.Authority) == 0 {
			continue
		}
		o.Authority = append(append([]ApprovalAuthorityDelta(nil), src.Authority...), o.Authority...)
		out = append(out, o)
	}
	return out
}

// quietGrantSource selects enabled, time-bounded authority, preferring the chat lease.
func quietGrantSource(primary []ApprovalOption) *ApprovalOption {
	wants := []ApprovalOptionRung{ApprovalRungChat, ApprovalRungDay, ApprovalRungOnce, ApprovalRungUnchanged}
	for _, want := range wants {
		for i := range primary {
			if primary[i].Rung == want && !primary[i].Disabled && strings.TrimSpace(primary[i].Group) == "" &&
				optionAuthorityTimeBounded(primary[i]) {
				return &primary[i]
			}
		}
	}
	return nil
}

// optionAuthorityTimeBounded reports whether copying this option's authority
// would stay inside a day or the chat.
func optionAuthorityTimeBounded(option ApprovalOption) bool {
	for _, delta := range option.Authority {
		if delta.Grant != nil && !delta.Grant.TimeBounded() {
			return false
		}
	}
	return true
}
