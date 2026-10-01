package security

import (
	"context"
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestConformanceSweepPassesAConformingAPI proves the sweep reports nothing
// against an API that keeps every invariant, so a finding is a real defect.
func TestConformanceSweepPassesAConformingAPI(t *testing.T) {
	t.Parallel()
	sweep := newWidgetSweep(t, widgetFaults{})
	sweep.runAll(t.Context())
	sweep.checkEventScopes([]wire.EventEnvelope{
		{Topic: wire.EventTopicProject, Scope: wire.EventScope{Kind: wire.EventScopeProject, ProjectID: fixtureWidgetID}},
		{Topic: wire.EventTopicSession, Scope: wire.EventScope{Kind: wire.EventScopeSession, ProjectID: fixtureWidgetID, SessionID: fixturePartID}},
		{Topic: wire.EventTopicSession, Scope: wire.EventScope{Kind: wire.EventScopeDevice}},
	}, widgetHostIDs())
	for _, rule := range conformanceRules {
		for _, v := range sweep.findings.list(rule) {
			t.Errorf("%s: %s", rule, v)
		}
	}
	if len(sweep.secrets.seeded) != 1 {
		t.Fatalf("seeded secrets = %v, want createPart", sweep.secrets.seeded)
	}
}

// TestConformanceSweepDetectsEachBrokenInvariant breaks one invariant at a
// time and requires the sweep to name it.
func TestConformanceSweepDetectsEachBrokenInvariant(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		faults widgetFaults
		rule   conformanceRule
		want   string
	}{
		{"undeclared status", widgetFaults{undeclaredStatus: true}, ruleResponsesConform,
			"getWidget: read: answered undeclared status 400 invalid_request"},
		{"generic not found", widgetFaults{genericNotFound: true}, ruleUnknownResources,
			`getWidget: unknown {id}: answered 404 "not_found", which does not name the resource`},
		{"child code wins over its parent", widgetFaults{childCodeWins: true}, ruleUnknownResources,
			"getPart: unknown {id}: answered source_pin_not_found, but an unknown /v1/widgets/{id} answers session_not_found (getWidget)"},
		{"media type ignored", widgetFaults{lenientBodies: true}, ruleRequestBodies,
			"createPart: unsupported media type: answered 400 invalid_request, want 415 unsupported_media_type"},
		{"malformed JSON accepted", widgetFaults{lenientBodies: true}, ruleRequestBodies,
			"createPart: malformed JSON: answered 400 invalid_request, want 400 invalid_json"},
		{"unknown field accepted", widgetFaults{lenientBodies: true}, ruleRequestBodies,
			"createPart: unknown field: answered 400 invalid_request, want 400 invalid_json"},
		{"body bound missing", widgetFaults{lenientBodies: true}, ruleRequestBodies,
			"createPart: oversized body: answered 400 invalid_request, want 413"},
		{"limit clamped", widgetFaults{clampsLimit: true}, rulePagination,
			`listWidgets: limit above maximum: answered 200 (details.param ""), want 400 invalid_query naming "limit"`},
		{"cursor ignored", widgetFaults{clampsLimit: true}, rulePagination,
			`listWidgets: foreign cursor: answered 200 (details.param ""), want 400 invalid_page_cursor naming "cursor"`},
		{"read writes", widgetFaults{writesOnRead: true}, ruleReadsHaveNoEffects,
			"getWidget: read: changed host state: store commit"},
		{"secret echoed", widgetFaults{echoesSecret: true}, ruleNoSecretMaterial,
			"getWidget: read: response body reveals the secret set through createPart.token"},
		{"raw error text", widgetFaults{rawMessage: true}, ruleMessageIsNoticeCopy,
			`getWidget: unknown {id}: session_not_found message is "sql: no rows in result set", not the notice copy "Widget not found."`},
		{"undeclared detail", widgetFaults{undeclaredDetail: true}, ruleDetailsAreDeclared,
			"getWidget: unknown {id}: session_not_found details.path is not declared by its notice context_schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sweep := newWidgetSweep(t, tc.faults)
			sweep.runAll(t.Context())
			found := sweep.findings.list(tc.rule)
			for _, v := range found {
				if v == tc.want {
					return
				}
			}
			t.Fatalf("%s findings do not include %q:\n%s", tc.rule, tc.want, strings.Join(found, "\n"))
		})
	}
}

// TestConformanceSweepDetectsEventScopeFaults requires a path, a non-UUID, and
// an id the host never registered to be reported.
func TestConformanceSweepDetectsEventScopeFaults(t *testing.T) {
	t.Parallel()
	sweep := newWidgetSweep(t, widgetFaults{})
	sweep.checkEventScopes([]wire.EventEnvelope{
		{Topic: wire.EventTopicProject, Scope: wire.EventScope{Kind: wire.EventScopeProject, ProjectID: "/Users/someone/project"}},
		{Topic: wire.EventTopicSession, Scope: wire.EventScope{Kind: wire.EventScopeSession, ProjectID: fixtureWidgetID, SessionID: fixtureWidgetID}},
		{Topic: wire.EventTopicSession, Scope: wire.EventScope{Kind: wire.EventScopeDevice, ProjectID: fixtureWidgetID}},
	}, widgetHostIDs())
	want := []string{
		`project: scope project_id: "/Users/someone/project" is not a UUID`,
		`session: device scope: carries project "` + fixtureWidgetID + `" session ""`,
		"session: scope session_id: " + fixtureWidgetID + " names nothing the host registered",
	}
	got := sweep.findings.list(ruleEventScopesAreHostIDs)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("event scope findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestConformanceSweepRequiresASeededSecret keeps the secret rule from
// passing when nothing accepted a sentinel.
func TestConformanceSweepRequiresASeededSecret(t *testing.T) {
	t.Parallel()
	sweep := newWidgetSweep(t, widgetFaults{})
	sweep.values.bind("/v1/widgets/{id}", "", "7f1c2a4e-0000-4000-8000-00000000ffff")
	sweep.seedSecrets(t.Context())
	got := sweep.findings.list(ruleNoSecretMaterial)
	want := "sweep: seed secret: no operation accepted a secret, so nothing was scanned for"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("secret findings = %q, want %q", got, want)
	}
}

func widgetHostIDs() conformanceHostIDs {
	return conformanceHostIDs{
		project: func(_ context.Context, id string) (bool, error) { return id == fixtureWidgetID, nil },
		session: func(_ context.Context, id string) (bool, error) { return id == fixturePartID, nil },
	}
}
