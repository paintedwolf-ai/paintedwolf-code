package main

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func loadBundled(t *testing.T) (Config, *toolschema.Config) {
	t.Helper()
	schemas, err := toolschema.LoadSchemaDir(schemaDirPath(configlayout.FindModuleRoot()))
	if err != nil {
		testutil.FailErr(t, "load tools/schemas", err)
	}
	cfg, err := Load(configFilePath(configlayout.FindModuleRoot()))
	if err != nil {
		testutil.FailErr(t, "load tool-presentation.yaml", err)
	}
	return cfg, schemas
}

func TestBundledPresentationValidates(t *testing.T) {
	cfg, schemas := loadBundled(t)
	if err := cfg.Validate(schemas); err != nil {
		testutil.FailErr(t, "validate bundled tool-presentation.yaml", err)
	}
}

func TestBundledPresentationCoversEverySchemaTool(t *testing.T) {
	cfg, schemas := loadBundled(t)
	if len(cfg.Tools) != len(schemas.Tools) {
		t.Fatalf("tool-presentation.yaml has %d tools, tools/schemas has %d — they must stay 1:1",
			len(cfg.Tools), len(schemas.Tools))
	}
}

func TestValidateRejectsUnknownTool(t *testing.T) {
	cfg, schemas := loadBundled(t)
	cfg.Tools["not_a_real_tool"] = Entry{}

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected an entry with no tools/schemas unit to fail validation")
	}
	if !strings.Contains(err.Error(), "not_a_real_tool") {
		t.Fatalf("error should name the unknown tool, got: %v", err)
	}
}

func TestValidateRejectsMissingEntry(t *testing.T) {
	cfg, schemas := loadBundled(t)
	delete(cfg.Tools, "read")

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected a schema tool with no presentation entry to fail validation")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Fatalf("error should name the uncovered tool, got: %v", err)
	}
}

func TestValidateRejectsTitleKeyThatIsNotAnArg(t *testing.T) {
	cfg, schemas := loadBundled(t)
	cfg.Tools["read"] = Entry{TitleKeys: []string{"nonexistent_arg"}}

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected a title key that is not a schema property to fail validation")
	}
	if !strings.Contains(err.Error(), "nonexistent_arg") {
		t.Fatalf("error should name the bad key, got: %v", err)
	}
}

func TestValidateRejectsRemovedCanonicalTitleKey(t *testing.T) {
	cfg, schemas := loadBundled(t)

	entry := schemas.Tools["grep"]
	props, _ := entry.Schema["properties"].(map[string]any)
	if _, ok := props["pattern"]; !ok {
		t.Fatal("fixture drift: grep declares no pattern property")
	}
	delete(props, "pattern")
	schemas.Tools["grep"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected a removed title key to fail validation")
	}
	if !strings.Contains(err.Error(), `tool "grep": title key "pattern"`) {
		t.Fatalf("error should name the removed grep title key, got: %v", err)
	}
}

func TestValidateRejectsTitleKeysOnTaskCardTool(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["task"]
	entry.TitleKeys = []string{"agent_type"}
	cfg.Tools["task"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected title_keys on a kind: task tool to fail validation")
	}
	if !strings.Contains(err.Error(), "dead data") {
		t.Fatalf("error should explain why TaskCard title keys are dead, got: %v", err)
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.Kind = "reading"
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected an unknown kind to fail validation")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Fatalf("error should name the bad kind, got: %v", err)
	}
}

func TestEveryActivityRoleHasATierAndEveryToolHasARole(t *testing.T) {
	for _, role := range ActivityRoles() {
		if role.Tier() <= 0 {
			t.Fatalf("role %q has no tier", role)
		}
	}
	cfg, _ := loadBundled(t)
	for name, entry := range cfg.Tools {
		if entry.Activity.Role == "" {
			t.Fatalf("tool %q declares no activity.role, so its span would rank as nothing", name)
		}
	}
}

func TestValidateRejectsUnknownActivityRole(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.Activity.Role = "reading"
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected an unknown activity.role to fail validation")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Fatalf("error should name the bad activity.role, got: %v", err)
	}
}

func TestValidateRejectsPathEntryWithoutPathArg(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["git_commit"]
	entry.PathEntry = PathEntryFile
	cfg.Tools["git_commit"] = entry

	err := cfg.Validate(schemas)
	if err == nil || !strings.Contains(err.Error(), "git_commit") {
		t.Fatalf("path_entry on a tool without a path arg validated: %v", err)
	}
}

func TestValidateRejectsUnknownPathEntry(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.PathEntry = "symlink"
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unknown path_entry validated: %v", err)
	}
}

func TestFileMutationToolsDeclareFilePathEntry(t *testing.T) {
	cfg, _ := loadBundled(t)
	entries := cfg.PathEntries()
	for _, name := range cfg.ToolsWithKind(KindWrite) {
		if entries[name] != PathEntryFile {
			t.Errorf("write-card tool %q path_entry = %q, want file", name, entries[name])
		}
	}
	if entries["list_dir"] != PathEntryFolder {
		t.Errorf("list_dir path_entry = %q, want folder", entries["list_dir"])
	}
}

func TestValidateRejectsLongRunningWorkerDispatch(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["task"]
	entry.Visibility = VisibilityLongRunning
	cfg.Tools["task"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected kind task + long_running to fail validation")
	}
	if !strings.Contains(err.Error(), "render and then vanish") {
		t.Fatalf("error should explain the withdrawn row, got: %v", err)
	}
}

func TestSurveyToolsStayGenericKind(t *testing.T) {
	cfg, _ := loadBundled(t)
	kinds := cfg.Kinds()

	for _, tool := range []string{"find", "list_dir", "stat", "wc", "jq"} {
		if kind, ok := kinds[tool]; ok {
			t.Errorf("tool %q should render generically, got kind %q", tool, kind)
		}
	}
	for tool, want := range map[string]Kind{
		"read": KindRead, "grep": KindRead,
		"write": KindWrite, "edit": KindWrite,
		"command": KindCommand, "task": KindTask, "delegate_dispatch": KindTask,
	} {
		if kinds[tool] != want {
			t.Errorf("tool %q kind = %q, want %q", tool, kinds[tool], want)
		}
	}
}

func TestValidateRejectsUnknownVisibility(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.Visibility = "sometimes"
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("expected an unknown visibility class to fail validation")
	}
	if !strings.Contains(err.Error(), "sometimes") {
		t.Fatalf("error should name the bad visibility, got: %v", err)
	}
}

func TestVisibilityClassMembership(t *testing.T) {
	cfg, _ := loadBundled(t)

	for _, tc := range []struct {
		vis  Visibility
		want []string
	}{
		{VisibilityCoordinatorInternal, []string{"git_status", "pack_board", "update_progress"}},
		{VisibilityLongRunning, []string{"command", "summarize", "survey_repo", "verify"}},
	} {
		got := cfg.ToolsWithVisibility(tc.vis)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("visibility %s = %v, want %v", tc.vis, got, tc.want)
		}
	}

	if got := cfg.ToolsWithKind(KindTask); strings.Join(got, ",") != "delegate_dispatch,task" {
		t.Errorf("kind task = %v, want delegate_dispatch, task", got)
	}
}

func TestUnsetVisibilityIsDefault(t *testing.T) {
	cfg, _ := loadBundled(t)

	named := map[string]bool{}
	for _, vis := range []Visibility{
		VisibilityCoordinatorInternal, VisibilityLongRunning,
	} {
		for _, tool := range cfg.ToolsWithVisibility(vis) {
			named[tool] = true
		}
	}
	for _, tool := range cfg.ToolsWithVisibility(VisibilityDefault) {
		if named[tool] {
			t.Errorf("tool %q is in both the default and a named visibility class", tool)
		}
		if cfg.Tools[tool].Visibility != "" {
			t.Errorf("tool %q has explicit visibility but landed in default", tool)
		}
	}
}

func TestValidateRejectsTitleKeyThatCannotProduceAString(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		key      string
		contains string
	}{
		{"object", "submit_verdict", "verdict", "an object"},
		{"integer", "wait", "timeout_ms", "an integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, schemas := loadBundled(t)
			entry := cfg.Tools[tc.tool]
			entry.TitleKeys = []string{tc.key}
			cfg.Tools[tc.tool] = entry

			err := cfg.Validate(schemas)
			if err == nil {
				t.Fatalf("title key %q on %q should fail validation", tc.key, tc.tool)
			}
			if !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error should name the key and its type (%q), got: %v", tc.contains, err)
			}
		})
	}
}

func TestValidateRejectsClosedEnumTitleKey(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["render_view"]
	entry.TitleKeys = []string{"mime"}
	cfg.Tools["render_view"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("a closed-enum title key should fail validation")
	}
	if !strings.Contains(err.Error(), "closed enum") {
		t.Fatalf("error should say the key is a closed enum, got: %v", err)
	}
}

func TestValidateAcceptsPrintableTitleKeyShapes(t *testing.T) {
	for _, tc := range []struct{ tool, key string }{
		{"copy", "copies"}, // array of {from,to}
		{"stat", "paths"},  // array of strings
		{"read", "path"},   // plain string
	} {
		cfg, schemas := loadBundled(t)
		entry := cfg.Tools[tc.tool]
		entry.TitleKeys = []string{tc.key}
		cfg.Tools[tc.tool] = entry

		if err := cfg.Validate(schemas); err != nil {
			t.Fatalf("title key %q on %q must stay legal: %v", tc.key, tc.tool, err)
		}
	}
}

func TestEveryToolDeclaresActivityPresentation(t *testing.T) {
	cfg, _ := loadBundled(t)
	for name, entry := range cfg.Tools {
		if entry.Activity.Headline == "" || entry.Activity.Salience == "" {
			t.Errorf("tool %q declares incomplete activity presentation", name)
		}
	}
}

func TestValidateRejectsMissingActivityHeadline(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.Activity.Headline = ""
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("a tool with no activity headline should fail validation")
	}
	if !strings.Contains(err.Error(), `tool "read": activity.headline is required`) {
		t.Fatalf("error should name the tool, got: %v", err)
	}
}

func TestValidateRejectsUnknownActivitySalience(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.Activity.Salience = "loud"
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("an unknown activity salience should fail validation")
	}
	if !strings.Contains(err.Error(), `unknown activity.salience "loud"`) {
		t.Fatalf("error should name the bad salience, got: %v", err)
	}
}

func TestValidateRejectsActivityHeadlineWhitespace(t *testing.T) {
	cfg, schemas := loadBundled(t)
	entry := cfg.Tools["read"]
	entry.Activity.Headline = " investigating "
	cfg.Tools["read"] = entry

	err := cfg.Validate(schemas)
	if err == nil {
		t.Fatal("an activity headline with surrounding whitespace should fail validation")
	}
	if !strings.Contains(err.Error(), "has surrounding whitespace") {
		t.Fatalf("error should explain the whitespace, got: %v", err)
	}
}

func TestActivityPresentationWeights(t *testing.T) {
	cfg, _ := loadBundled(t)
	activities := cfg.ActivityPresentations()

	for tool, want := range map[string]struct {
		headline string
		weight   int
	}{
		"update_progress": {headline: "coordinating", weight: 0},
		"read":            {headline: "investigating", weight: 1},
		"command":         {headline: "running commands", weight: 3},
		"edit":            {headline: "making changes", weight: 6},
	} {
		activity := activities[tool]
		if activity.Headline != want.headline || activity.Weight() != want.weight {
			t.Errorf("activity for %q = %+v (weight %d), want %+v", tool, activity, activity.Weight(), want)
		}
	}
	if len(activities) != len(cfg.Tools) {
		t.Errorf("activity presentations cover %d tools, want %d", len(activities), len(cfg.Tools))
	}
}

func TestLoadRejectsUnknownEntryField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-presentation.yaml")
	body := "fallback_title_keys: [path]\ntools:\n  read:\n    run_label: \"Reading…\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		testutil.FailErr(t, "write fixture", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a misspelled entry field should fail Load, not be dropped")
	}
}
