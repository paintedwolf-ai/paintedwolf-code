package usernotice

import (
	"strings"
	"testing"
)

func TestNotificationFloorRejectsOverriddenTier(t *testing.T) {
	// not_found is a stock code declared non_catastrophic/session.
	entry, err := ParseEntry([]byte(
		"surfaces: [http]\n" +
			"notification: { tier: catastrophic, scope: app }\n" +
			"title: selected\nmessage: selected msg\n",
	))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	err = enforceNotificationFloor("not_found", entry)
	if err == nil {
		t.Fatal("expected the floor to reject an overridden tier")
	}
	if !strings.Contains(err.Error(), "host-managed") {
		t.Fatalf("error should name the floor, got %v", err)
	}
}

func TestNotificationFloorRejectsDroppedBlock(t *testing.T) {
	entry, err := ParseEntry([]byte("surfaces: [http]\ntitle: selected\nmessage: selected msg\n"))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if err := enforceNotificationFloor("not_found", entry); err == nil {
		t.Fatal("expected the floor to reject a missing notification block")
	}
}

func TestNotificationFloorAllowsCopyOverride(t *testing.T) {
	entry, err := ParseEntry([]byte(
		"surfaces: [http]\n" +
			"notification: { tier: non_catastrophic, scope: session }\n" +
			"title: our own words\nmessage: our own message\n",
	))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if err := enforceNotificationFloor("not_found", entry); err != nil {
		t.Fatalf("copy-only override must be allowed: %v", err)
	}
}

// The floor compares the whole block, so a conditional code cannot be flattened
// into a single tier either.
func TestNotificationFloorRejectsFlattenedResolutions(t *testing.T) {
	entry, err := ParseEntry([]byte(
		"surfaces: [preflight]\n" +
			"notification: { tier: non_catastrophic, scope: app }\n" +
			"title: t\nmessage: m\n",
	))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if err := enforceNotificationFloor("OS_BELOW_FLOOR", entry); err == nil {
		t.Fatal("expected the floor to reject flattened resolutions")
	}
}

// A code a pack introduces has no stock floor to match, but Validate still
// requires it to declare tier and scope.
func TestNotificationFloorIgnoresPackIntroducedCode(t *testing.T) {
	entry, err := ParseEntry([]byte(
		"surfaces: [http]\n" +
			"notification: { tier: non_catastrophic, scope: session }\n" +
			"title: t\nmessage: m\n",
	))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if err := enforceNotificationFloor("acme_only_code", entry); err != nil {
		t.Fatalf("pack-introduced code must load: %v", err)
	}
}

// The discriminator resolves by exact string equality on one context key.
func TestNotificationResolve(t *testing.T) {
	n := Notification{
		Discriminator: "reason",
		Resolutions: []Resolution{
			{ID: "below_floor", When: "below_floor", Tier: TierCatastrophic, Scope: ScopeApp},
			{ID: "unreadable", When: "unreadable", Tier: TierNonCatastrophic, Scope: ScopeApp},
		},
	}
	for _, tc := range []struct {
		name string
		ctx  map[string]any
		want string
		ok   bool
	}{
		{"blocked branch", map[string]any{"reason": "below_floor"}, TierCatastrophic, true},
		{"degraded branch", map[string]any{"reason": "unreadable"}, TierNonCatastrophic, true},
		{"no fallback declared", map[string]any{"reason": "something_else"}, "", false},
		{"missing key", map[string]any{}, "", false},
		{"non-string value", map[string]any{"reason": 3}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := n.Resolve(tc.ctx)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if ok && got.Tier != tc.want {
				t.Fatalf("tier=%q want %q", got.Tier, tc.want)
			}
		})
	}
}

func TestNotificationResolveUsesFallback(t *testing.T) {
	n := Notification{
		Discriminator: "reason",
		Resolutions: []Resolution{
			{ID: "hard", When: "hard", Tier: TierCatastrophic, Scope: ScopeApp},
			{ID: "other", Tier: TierNonCatastrophic, Scope: ScopeApp},
		},
	}
	got, ok := n.Resolve(map[string]any{"reason": "unmatched"})
	if !ok || got.ID != "other" {
		t.Fatalf("Resolve = %#v, %v; want the fallback", got, ok)
	}
}

func TestNotificationNormalizedSugar(t *testing.T) {
	n := Notification{Tier: TierNonCatastrophic, Scope: ScopeSession}
	res := n.Normalized()
	if len(res) != 1 || res[0].ID != DefaultResolutionID {
		t.Fatalf("Normalized = %#v", res)
	}
	got, ok := n.Resolve(nil)
	if !ok || got.Scope != ScopeSession {
		t.Fatalf("Resolve = %#v, %v", got, ok)
	}
}
