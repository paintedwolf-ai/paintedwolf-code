package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/theme"
	api "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A YAML pack reaches every contribution surface.
func TestThirdPartyPackReachesEverySurface(t *testing.T) {
	t.Parallel()

	pack := contractFixturePack(t, "acme/review-kit", map[string]string{
		"contributions/commands/review-selection.yaml": strings.Join([]string{
			"id: acme/review-kit:review-selection",
			`title: "Review this selection"`,
			`scope: "files"`,
			"keywords:",
			`  - "critique"`,
			"when:",
			"  fact: editor_active",
			"action:",
			"  kind: editor_action",
			"  ref: acme/review-kit:action-review",
			"",
		}, "\n"),
		"contributions/editor-actions/action-review.yaml": strings.Join([]string{
			"id: acme/review-kit:action-review",
			`title: "Review this selection"`,
			"target:",
			`  kind: "selection"`,
			"  required: true",
			"execution:",
			`  preset: "inspect_file"`,
			`  prompt_ref: "guidance/review-selection"`,
			"",
		}, "\n"),
		"guidance/review-selection.md": "Review the selected code.\n",
		"contributions/keybindings/key-review-selection.yaml": strings.Join([]string{
			"id: acme/review-kit:key-review-selection",
			"command: acme/review-kit:review-selection",
			`scope: "files"`,
			"bindings:",
			"  macos:",
			`    - "Mod+Shift+R"`,
			"  windows:",
			`    - "Mod+Shift+R"`,
			"  linux:",
			`    - "Mod+Shift+R"`,
			"",
		}, "\n"),
		"contributions/menus/app-menu-edit-review.yaml": strings.Join([]string{
			"id: acme/review-kit:app-menu-edit-review",
			`slot: "app_menu.edit"`,
			"command: acme/review-kit:review-selection",
			`group: "9"`,
			"order: 1",
			`label: "Review Selection"`,
			"",
		}, "\n"),
	})

	frame := frameWithPacks(t, pack)
	projected := contribframe.Project(frame)

	command := findProjectedCommand(t, projected.Commands, "acme/review-kit:review-selection")
	if command.Provider != "acme/review-kit" {
		t.Errorf("provider = %q, want acme/review-kit", command.Provider)
	}
	if command.Executor != "host" || command.Invocation != "session" {
		t.Errorf("executor/invocation = %s/%s, want host/session", command.Executor, command.Invocation)
	}
	if command.ActionRef != "acme/review-kit:action-review" {
		t.Errorf("action_ref = %q, want the declared editor action", command.ActionRef)
	}
	if command.When == nil || command.When.Fact != "editor_active" {
		t.Error("the declared condition did not reach the frame")
	}
	if command.Palette != nil && !*command.Palette {
		t.Error("an ordinary command must be in the palette unless it opts out")
	}

	slots := map[string]bool{}
	for _, menu := range projected.Menus {
		if menu.Command == command.ID {
			slots[menu.Slot] = true
		}
	}
	if !slots["app_menu.edit"] {
		t.Error("the pack's app_menu.edit placement is not in the frame")
	}

	for _, platform := range []string{"macos", "windows", "linux"} {
		var active string
		for _, def := range projected.BindingDefaults {
			if def.Platform == platform && def.Chord == "Mod+Shift+R" {
				active = def.Active
			}
		}
		if active != "acme/review-kit:key-review-selection" {
			t.Errorf("%s: Mod+Shift+R resolves to %q, want the pack's binding", platform, active)
		}
	}

	action := findProjectedEditorAction(t, projected.EditorActions, "acme/review-kit:action-review")
	if action.Preset != string(contribution.PresetInspectFile) {
		t.Errorf("preset = %q, want inspect_file", action.Preset)
	}
	if !action.TargetRequired || action.TargetKind != string(contribution.TargetSelection) {
		t.Errorf("target = %s/required=%v, want selection/required", action.TargetKind, action.TargetRequired)
	}
}

func frameWithPacks(t *testing.T, packs ...extpacks.PackContent) *contribframe.Frame {
	t.Helper()
	stock, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "discover stock content", err)
	desired := extpacks.EmptyDesired()
	for _, pc := range packs {
		desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: pc.Pack.ID})
	}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   append(stock, packs...),
		Desired: desired,
	})
	testutil.FailErr(t, "resolve with pack", eff.BootError())
	view, err := catalogview.Build(t.Context(), filepath.Join(contractcheck.RepoRoot(t), "lycaon"), eff)
	testutil.FailErr(t, "build view", err)
	frame, err := contribframe.Build(view, &mcp.ResourceGeneration{Revision: "test"})
	testutil.FailErr(t, "build frame", err)
	return frame
}

func contractFixturePack(t *testing.T, id string, files map[string]string) extpacks.PackContent {
	t.Helper()
	root := filepath.Join(t.TempDir(), strings.ReplaceAll(id, "/", "-"))
	manifest := "manifest_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	testutil.FailErr(t, "mkdir pack", os.MkdirAll(root, 0o755))
	testutil.FailErr(t, "write manifest",
		os.WriteFile(filepath.Join(root, "extension.yaml"), []byte(manifest), 0o644))
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir unit dir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write unit", os.WriteFile(path, []byte(body), 0o644))
	}
	man, err := extpacks.LoadManifest(root)
	testutil.FailErr(t, "load manifest", err)
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: id, Root: extpacks.OnDisk(root)}, man)
	testutil.FailErr(t, "inventory pack", err)
	return pc
}

func findProjectedCommand(t *testing.T, rows []api.ContributionCommand, id string) api.ContributionCommand {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("command %s is not in the projected frame", id)
	return api.ContributionCommand{}
}

func findProjectedEditorAction(t *testing.T, rows []api.ContributionEditorAction, id string) api.ContributionEditorAction {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("editor action %s is not in the projected frame", id)
	return api.ContributionEditorAction{}
}

// A theme has no behavior, so this proves a pack of pure color reaches the shell
// with total values, the host filling everything the author left out.
func TestThirdPartyThemeReachesTheFrameTotal(t *testing.T) {
	t.Parallel()

	pack := contractFixturePack(t, "acme/nightshade", map[string]string{
		"contributions/themes/nightshade.yaml": strings.Join([]string{
			"id: acme/nightshade:nightshade",
			"name: Nightshade",
			"appearance: dark",
			"tokens:",
			`  background: "#14121a"`,
			`  text: "#e2ddf0"`,
			`  accent: "#a070d8"`,
			`  on-accent: "#000000"`,
			`  on-accent-warm: "#000000"`,
			`  danger: "#ff6b81"`,
			`  warning: "#e8b04b"`,
			`  status-positive: "#6fd0a0"`,
			"  # Colorblind-safe diffs without moving what success means.",
			`  diff-add-hue: "#7aa2f7"`,
			`  diff-delete-hue: "#e8b04b"`,
			"syntax:",
			`  keyword: "#c792ea"`,
			"  comment:",
			`    color: "#8b93b8"`,
			"    italic: true",
			"",
		}, "\n"),
	})

	projected := contribframe.Project(frameWithPacks(t, pack))
	var row *api.ContributionTheme
	for i := range projected.Themes {
		if projected.Themes[i].ID == "acme/nightshade:nightshade" {
			row = &projected.Themes[i]
		}
	}
	if row == nil {
		t.Fatal("the pack's theme is not in the projected frame")
	}
	if row.Name != "Nightshade" || row.Appearance != "dark" {
		t.Errorf("name/appearance = %s/%s", row.Name, row.Appearance)
	}

	// Total: the shell derives nothing, so anything absent here paints nothing.
	for _, tok := range theme.BaseTokens() {
		if row.Tokens[tok.ID] == "" {
			t.Errorf("token %s is absent from the projection", tok.ID)
		}
	}
	for _, scope := range theme.SyntaxScopes() {
		if row.Syntax[scope.ID].Color == "" {
			t.Errorf("syntax scope %s is absent from the projection", scope.ID)
		}
	}

	// Authored values survive; unset ones carry their host derivation or fold.
	if row.Tokens["diff-add-hue"] != "#7aa2f7" {
		t.Errorf("diff-add-hue = %s, want the authored blue", row.Tokens["diff-add-hue"])
	}
	if row.Tokens["status-positive"] != "#6fd0a0" {
		t.Errorf("status-positive = %s — a diff hue must not move success",
			row.Tokens["status-positive"])
	}
	if row.Tokens["surface"] != "#14121a" {
		t.Errorf("surface = %s, want the background it derives from", row.Tokens["surface"])
	}
	if got := row.Syntax["keyword.control"]; got.Color != "#c792ea" {
		t.Errorf("keyword.control = %s, want the parent it folds to", got.Color)
	}
	if got := row.Syntax["comment"]; !got.Italic {
		t.Error("the authored italic flag did not reach the frame")
	}
}
