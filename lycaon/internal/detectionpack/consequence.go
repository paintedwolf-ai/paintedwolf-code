package detectionpack

import (
	"fmt"
	"strings"
)

// Structured consequence / presence vocabularies projected onto tool_exec events.
// These particular fields are host-derived from connector/catalog mappings; they are
// not model-declared. That is scoped to this vocabulary and says nothing about the
// rest of the event: CommandLine, Argv, and DeclaredDestination carry model-authored
// text and are ordinary rule material.
const (
	PresenceTrue    = "true"
	PresenceFalse   = "false"
	PresenceUnknown = "unknown"
)

var allowedActionEffects = map[string]struct{}{
	"create": {}, "update": {}, "delete": {}, "purge": {}, "grant": {}, "revoke": {},
	"disable": {}, "transfer": {}, "charge": {}, "refund": {}, "payout": {}, "send": {},
	"publish": {}, "unpublish": {}, "deprecate": {}, "yank": {}, "rotate": {}, "export": {},
	"cancel": {}, "encrypt": {}, "execute": {}, "expose": {}, "lockout": {}, "notify": {},
	"provision": {}, "restore": {}, "share": {}, "read": {}, "deploy": {}, "stop": {},
	"submit": {}, "distribute": {},
	"unknown": {},
}

var allowedTargetScopes = map[string]struct{}{
	"item": {}, "collection": {}, "namespace": {}, "dataset": {}, "project": {},
	"account": {}, "organization": {}, "global": {}, "campaign": {}, "channel": {},
	"domain": {}, "recipient": {}, "resource": {}, "service": {}, "subscription": {},
	"tenant": {}, "secret": {}, "identity": {}, "role": {}, "backup": {},
	"snapshot": {}, "package": {}, "release": {}, "image": {},
	"workflow": {}, "environment": {}, "protection": {}, "application": {},
	"branch": {}, "tag": {}, "group": {}, "unknown": {},
}

var allowedPrincipalScopes = map[string]struct{}{
	"named": {}, "scoped": {}, "cross_account": {}, "public": {}, "anonymous": {},
	"wildcard": {}, "unknown": {},
}

var allowedCredentialPersistence = map[string]struct{}{
	"session": {}, "long_lived": {}, "signing": {}, "root": {}, "unknown": {},
}

var allowedPresence = map[string]struct{}{
	PresenceTrue: {}, PresenceFalse: {}, PresenceUnknown: {},
}

var allowedEgressTransport = map[string]struct{}{
	"http_request": {}, "http_connect": {}, "socks_tcp": {},
}

var allowedEgressOrigin = map[string]struct{}{
	"confined_proxy": {}, "attributed_host": {},
}

var allowedDecisionStage = map[string]struct{}{
	"pre_dial": {}, "outcome": {},
}

// EffectReach vocabulary — host-derived destination class for tool_exec.
const (
	EffectReachLocal    = "local"
	EffectReachRemote   = "remote"
	EffectReachUnproven = "unproven"
)

var allowedEffectReach = map[string]struct{}{
	EffectReachLocal: {}, EffectReachRemote: {}, EffectReachUnproven: {},
}

// typedFieldAllowlists validates selection values for closed consequence fields.
var typedFieldAllowlists = map[string]map[string]struct{}{
	"ActionEffect":          allowedActionEffects,
	"TargetScope":           allowedTargetScopes,
	"PrincipalScope":        allowedPrincipalScopes,
	"CredentialPersistence": allowedCredentialPersistence,
	"BulkAction":            allowedPresence,
	"AmountPresent":         allowedPresence,
	"TargetPresent":         allowedPresence,
	"Transport":             allowedEgressTransport,
	"Origin":                allowedEgressOrigin,
	"DecisionStage":         allowedDecisionStage,
	"EffectReach":           allowedEffectReach,
}

func validateTypedSelectionValues(field string, values []string) error {
	allowed, ok := typedFieldAllowlists[field]
	if !ok {
		return nil
	}
	for _, v := range values {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(v))]; !ok {
			return fmt.Errorf("unsupported value %q for field %s", v, field)
		}
	}
	return nil
}

// ProjectConsequenceEnums keeps only allowlisted values, lowercased, deduped, sorted, bounded.
func ProjectConsequenceEnums(field string, values []string, limit int) []string {
	allowed, ok := typedFieldAllowlists[field]
	if !ok {
		return canonicalBounded(values, limit)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := allowed[v]; !ok {
			continue
		}
		out = append(out, v)
	}
	return canonicalBounded(out, limit)
}

// ProjectPresence keeps true/false/unknown; empty and unrecognized become "".
func ProjectPresence(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := allowedPresence[v]; ok {
		return v
	}
	return ""
}

// ProjectEffectReach keeps local/remote/unproven; empty and unrecognized become unproven.
func ProjectEffectReach(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := allowedEffectReach[v]; ok {
		return v
	}
	return EffectReachUnproven
}
