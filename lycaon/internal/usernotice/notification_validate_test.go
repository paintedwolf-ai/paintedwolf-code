package usernotice

import (
	"strings"
	"testing"
)

func entryWith(surfaces []string, n *Notification) Entry {
	return Entry{
		Surfaces:     surfaces,
		Notification: n,
		Title:        "t",
		Message:      "m",
	}
}

type notificationCase struct {
	name  string
	entry Entry
	want  string // substring; empty means it must validate
}

func runNotificationCases(t *testing.T, cases []notificationCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNotification("some_code", tc.entry)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateNotification: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// Which tier and scope a condition may claim, and how that interacts with the
// surface it is delivered on.
func TestValidateNotificationTierScope(t *testing.T) {
	invisible := false
	runNotificationCases(t, []notificationCase{
		{
			name:  "visible entry must declare a notification",
			entry: entryWith([]string{"http"}, nil),
			want:  "must declare notification",
		},
		{
			name:  "declared tier and scope validate",
			entry: entryWith([]string{"http"}, &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession}),
		},
		{
			name:  "unknown tier is rejected",
			entry: entryWith([]string{"http"}, &Notification{Tier: "annoying", Scope: ScopeSession}),
			want:  "unknown tier",
		},
		{
			name:  "unknown scope is rejected",
			entry: entryWith([]string{"http"}, &Notification{Tier: TierNonCatastrophic, Scope: "window"}),
			want:  "unknown scope",
		},
		{
			// A stop the user meets before anything renders can only come from the
			// readiness report — an HTTP body needs a request that has not happened.
			name:  "catastrophic must be preflight-only",
			entry: entryWith([]string{"http"}, &Notification{Tier: TierCatastrophic, Scope: ScopeApp}),
			want:  "surfaces must be exactly [preflight]",
		},
		{
			name:  "catastrophic must be app-scoped",
			entry: entryWith([]string{"preflight"}, &Notification{Tier: TierCatastrophic, Scope: ScopeSession}),
			want:  "scope must be",
		},
		{
			name:  "catastrophic preflight validates",
			entry: entryWith([]string{"preflight"}, &Notification{Tier: TierCatastrophic, Scope: ScopeApp}),
		},
		{
			// The readiness report is a whole-host answer with no session on it.
			name:  "session scope cannot ride preflight",
			entry: entryWith([]string{"preflight"}, &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession}),
			want:  "not deliverable",
		},
		{
			name:  "project scope on preflight validates",
			entry: entryWith([]string{"preflight"}, &Notification{Tier: TierNonCatastrophic, Scope: ScopeProject}),
		},
		{
			name: "user_visible false must not declare notification",
			entry: Entry{
				UserVisible:  &invisible,
				Notification: &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession},
			},
			want: "must not declare notification",
		},
		{
			name:  "user_visible false without notification validates",
			entry: Entry{UserVisible: &invisible},
		},
	})
}

// The shape of the conditional form: one discriminator, unique ids, at most one
// fallback, and the key actually documented.
func TestValidateNotificationResolutionShape(t *testing.T) {
	conditional := func(n *Notification, schema map[string]any) Entry {
		return Entry{
			Surfaces: []string{"http"}, Title: "t", Message: "m",
			ContextSchema: schema, Notification: n,
		}
	}
	twoWays := []Resolution{
		{ID: "a", When: "a", Tier: TierNonCatastrophic, Scope: ScopeSession},
		{ID: "b", When: "b", Tier: TierNonCatastrophic, Scope: ScopeSession},
	}
	documented := map[string]any{"required": []any{"reason"}}

	runNotificationCases(t, []notificationCase{
		{
			name: "discriminator without resolutions is rejected",
			entry: entryWith([]string{"http"}, &Notification{
				Tier: TierNonCatastrophic, Scope: ScopeSession, Discriminator: "reason",
			}),
			want: "discriminator requires resolutions",
		},
		{
			name:  "resolutions without discriminator are rejected",
			entry: entryWith([]string{"http"}, &Notification{Resolutions: twoWays}),
			want:  "must set discriminator",
		},
		{
			name: "resolutions must not also set tier",
			entry: conditional(&Notification{
				Tier: TierNonCatastrophic, Discriminator: "reason", Resolutions: twoWays,
			}, documented),
			want: "must not also set tier/scope",
		},
		{
			name: "undocumented discriminator is rejected",
			entry: conditional(&Notification{
				Discriminator: "reason", Resolutions: twoWays,
			}, nil),
			want: "documented in context_schema",
		},
		{
			name: "duplicate resolution ids are rejected",
			entry: conditional(&Notification{
				Discriminator: "reason",
				Resolutions: []Resolution{
					{ID: "same", When: "a", Tier: TierNonCatastrophic, Scope: ScopeSession},
					{ID: "same", When: "b", Tier: TierNonCatastrophic, Scope: ScopeSession},
				},
			}, documented),
			want: "duplicate notification resolution id",
		},
		{
			name: "two fallbacks are rejected",
			entry: conditional(&Notification{
				Discriminator: "reason",
				Resolutions: []Resolution{
					{ID: "a", Tier: TierNonCatastrophic, Scope: ScopeSession},
					{ID: "b", Tier: TierNonCatastrophic, Scope: ScopeSession},
				},
			}, documented),
			want: "at most one fallback",
		},
		{
			name: "a single resolution must use the tier/scope form",
			entry: conditional(&Notification{
				Discriminator: "reason",
				Resolutions:   []Resolution{{ID: "only", When: "a", Tier: TierNonCatastrophic, Scope: ScopeSession}},
			}, documented),
			want: "at least two",
		},
	})
}

func TestValidateNotificationScenarioCoverage(t *testing.T) {
	base := func(scenarios []ScenarioEntry) Entry {
		return Entry{
			Surfaces: []string{"http"}, Title: "t", Message: "m",
			ContextSchema: map[string]any{"required": []any{"reason"}},
			Notification: &Notification{
				Discriminator: "reason",
				Resolutions: []Resolution{
					{ID: "a", When: "a", Tier: TierNonCatastrophic, Scope: ScopeSession},
					{ID: "b", When: "b", Tier: TierNonCatastrophic, Scope: ScopeSession},
				},
			},
			Scenarios: scenarios,
		}
	}

	if err := validateNotification("c", base([]ScenarioEntry{
		{ID: "one", Resolution: "a"},
	})); err == nil || !strings.Contains(err.Error(), `resolution "b" has no scenario`) {
		t.Fatalf("uncovered resolution should fail, got %v", err)
	}

	if err := validateNotification("c", base([]ScenarioEntry{
		{ID: "one", Resolution: "a"}, {ID: "two", Resolution: "nope"},
	})); err == nil || !strings.Contains(err.Error(), "unknown resolution") {
		t.Fatalf("unknown resolution should fail, got %v", err)
	}

	if err := validateNotification("c", base([]ScenarioEntry{
		{ID: "one", Resolution: "a"}, {ID: "two"},
	})); err == nil || !strings.Contains(err.Error(), "must name a resolution") {
		t.Fatalf("unnamed scenario should fail, got %v", err)
	}

	if err := validateNotification("c", base([]ScenarioEntry{
		{ID: "one", Resolution: "a"}, {ID: "two", Resolution: "b"},
	})); err != nil {
		t.Fatalf("full coverage must validate: %v", err)
	}
}

// An unconditional notice may carry several fixtures.
func TestValidateNotificationUnconditionalScenarios(t *testing.T) {
	entry := entryWith([]string{"http"}, &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession})
	entry.Scenarios = []ScenarioEntry{{ID: "with_name"}, {ID: "nameless"}}
	if err := validateNotification("c", entry); err != nil {
		t.Fatalf("validateNotification: %v", err)
	}

	entry.Scenarios = []ScenarioEntry{{ID: "x", Resolution: "ghost"}}
	if err := validateNotification("c", entry); err == nil {
		t.Fatal("a scenario naming a resolution that does not exist should fail")
	}
}
