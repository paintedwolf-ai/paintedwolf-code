package usernotice

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadUserNoticeConfig(t *testing.T) {
	cfg, err := LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load host/user-notices", err)
	if len(HostErrorCodes(cfg)) < 7 {
		t.Fatalf("HostErrorCodes = %v want at least 7 host_error entries", HostErrorCodes(cfg))
	}
	if len(HTTPCodes(cfg)) < 40 {
		t.Fatalf("HTTPCodes = %d want full HTTP ledger coverage", len(HTTPCodes(cfg)))
	}
	entry, ok := cfg.UserNotices["session_aborted"]
	if !ok || entry.IsUserVisible() {
		t.Fatal("session_aborted must be user_visible:false")
	}
}

func TestValidateRejectsInvisibleWithSurfaces(t *testing.T) {
	visible := false
	cfg := &Config{
		Defaults: NoticeCopy{Title: "t", Message: "m"},
		UserNotices: map[string]Entry{
			"bad_code": {
				UserVisible: &visible,
				Surfaces:    []string{"host_error"},
			},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected validation error for invisible entry with surfaces")
	}
}

func TestValidateUseDefaultsEntry(t *testing.T) {
	useDefaults := true
	cfg := &Config{
		Defaults: NoticeCopy{Title: "t", Message: "m", SuggestedAction: "fix"},
		UserNotices: map[string]Entry{
			"invalid_json": {
				UseDefaults:  &useDefaults,
				Surfaces:     []string{"http"},
				Notification: &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession},
			},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	catalog := NewCatalog(cfg)
	copy := catalog.RenderWire("invalid_json", nil)
	if copy.Title != cfg.Defaults.Title || copy.Message != cfg.Defaults.Message {
		t.Fatalf("RenderWire = %#v want defaults", copy)
	}
}

func TestValidateRejectsUseDefaultsWithCopyFields(t *testing.T) {
	useDefaults := true
	cfg := &Config{
		Defaults: NoticeCopy{Title: "t", Message: "m"},
		UserNotices: map[string]Entry{
			"invalid_json": {
				UseDefaults: &useDefaults,
				Surfaces:    []string{"http"},
				Title:       "nope",
			},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected validation error for use_defaults with title")
	}
}

func TestValidateRejectsMissingCopy(t *testing.T) {
	cfg := &Config{
		Defaults: NoticeCopy{Title: "t", Message: "m"},
		UserNotices: map[string]Entry{
			"prompt_failed": {
				Surfaces: []string{"host_error"},
			},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected validation error for missing title/message")
	}
}

func TestValidateRejectsUnknownAction(t *testing.T) {
	cfg := &Config{
		Defaults: NoticeCopy{Title: "t", Message: "m"},
		UserNotices: map[string]Entry{
			"prompt_failed": {
				Surfaces:        []string{"host_error"},
				Title:           "t",
				Message:         "m",
				Action:          "infer_this_from_copy",
				SuggestedAction: "Act",
				Notification:    &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession},
			},
		},
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unknown action = %v", err)
	}
}

func TestValidateRendersTemplatedActionsForEveryTurnProgress(t *testing.T) {
	entry := func(action string) *Config {
		return &Config{
			Defaults: NoticeCopy{Title: "t", Message: "m"},
			UserNotices: map[string]Entry{"prompt_failed": {
				Surfaces: []string{"host_error"}, Title: "t", Message: "m", SuggestedAction: "Act",
				Notification: &Notification{Tier: TierNonCatastrophic, Scope: ScopeSession},
				Actions:      []string{action},
			}},
		}
	}
	valid := `{% if turn_progress == "made" %}prompt_keep_going{% elif turn_progress == "none" %}prompt_retry{% endif %}`
	testutil.FailErr(t, "validate progress-dependent actions", Validate(entry(valid)))
	// Only the progressed rendering names an unknown action.
	invalid := `{% if turn_progress == "made" %}keep_going_somehow{% else %}prompt_retry{% endif %}`
	if err := Validate(entry(invalid)); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("templated action rendering an unknown action = %v", err)
	}
}

func TestHostErrorCodesSortedUnique(t *testing.T) {
	cfg, err := LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load host/user-notices", err)
	codes := HostErrorCodes(cfg)
	for i := 1; i < len(codes); i++ {
		if codes[i] <= codes[i-1] {
			t.Fatalf("HostErrorCodes not sorted: %v", codes)
		}
	}
}
