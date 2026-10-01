import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";
import { loadSharedAppState } from "../../../store/app-state-snapshot.ts";
import {
  DEN_EDITOR_FONT_FAMILY_OPTIONS,
  editorKeymapPref, editorTabMovesFocusPref, saveEditorKeymap, saveEditorTabMovesFocus,
  cssFontFamilyForEditor,
  editorAgentActivityKindsPref,
  editorAgentActivityPref,
  editorFontFamilyPref,
  editorFontIsGuaranteed,
  editorFontSizePref,
  editorIndentGuidesPref,
  editorIndentStylePref,
  editorIndentWidthPref,
  editorLineHeightPref,
  editorLineNumbersPref,
  editorScrollbarTickKindsPref,
  editorScrollbarTicksPref,
  editorWhitespacePref,
  editorWordWrapPref,
  editorRevealInTreePref,
  editorTreeVisibleLevelsPref,
  saveEditorTreeVisibleLevels,
  editorVersionComparisonPref,
  editorWalkControlsDefaultPref,
  editorWalkControlsLocationPref,
  saveEditorWalkControlsDefault,
  saveEditorWalkControlsLocation,
  type EditorAgentActivityKind,
  type EditorScrollbarTickKind,
  saveEditorAgentActivity,
  saveEditorAgentActivityKind,
  saveEditorFontFamily,
  saveEditorFontSize,
  saveEditorIndentGuides,
  saveEditorIndentStyle,
  saveEditorIndentWidth,
  saveEditorLineHeight,
  saveEditorLineNumbers,
  saveEditorScrollbarTickKind,
  saveEditorScrollbarTicks,
  saveEditorWhitespace,
  saveEditorWordWrap,
  saveEditorRevealInTree,
  saveEditorVersionComparison,
  syncEditorPrefsFromSnapshot,
} from "../../../settings/editor/editor-prefs.ts";
import {
  externalOpenPrefs,
  saveExternalOpenPrefs,
  syncExternalOpenPrefsFromSnapshot,
  EDITOR_LABELS,
} from "../../../settings/editor/external-open-prefs.ts";
import { sanitizeFontFamily } from "../../../fonts/font-catalog.ts";
import { probeFontAvailability } from "../../../fonts/font-availability.ts";
import { tauriPlatform } from "../../../platform/runtime.ts";
import { detectEditors } from "../../../platform/files/detect-editors.ts";
import { hostSharesDevice } from "../../../platform/connection/host-identity.ts";
import {
  type LoadState,
  errorOf,
  isLoaded,
  loadFailed,
  loaded,
  unloaded,
  valueOf,
} from "../../../store/load-state.ts";
import { DEN_FILES_TREE_LEVELS } from "../../../../shared/app-state-types.ts";
import {
  settingAnchor,
  settingLabel,
  type SettingId,
} from "../../../settings/settings-registry.ts";
import type {
  BrowserPreset,
  DenEditorIndentStyle,
  ExternalEditorPreset,
  FoundEditor,
} from "../../../../shared/app-state-types.ts";

const SCROLLBAR_TICK_ROWS: ReadonlyArray<{
  kind: EditorScrollbarTickKind;
  setting: SettingId;
  hint: string;
}> = [
  { kind: "symbols", setting: "scrollbar-ticks-symbols", hint: "Declarations such as functions and classes." },
  { kind: "changes", setting: "scrollbar-ticks-changes", hint: "Added and removed lines in the shown comparison." },
  { kind: "matches", setting: "scrollbar-ticks-matches", hint: "Find results and other uses of the selected text." },
  { kind: "cursor", setting: "scrollbar-ticks-cursor", hint: "The caret, selection, or active Find result." },
  { kind: "windows", setting: "scrollbar-ticks-windows", hint: "Other windows’ carets and selections, in their window colors." },
  { kind: "agents", setting: "scrollbar-ticks-agents", hint: "Lines agents read and changes about to land, in agent colors." },
];

const AGENT_ACTIVITY_ROWS: ReadonlyArray<{
  kind: EditorAgentActivityKind;
  setting: SettingId;
  hint: string;
}> = [
  { kind: "reads", setting: "agent-activity-reads", hint: "Lines, whole files, and search matches a chat’s tools returned this turn." },
  { kind: "changes", setting: "agent-activity-changes", hint: "Lines an agent is about to edit or is waiting for approval to edit, and worker drafts." },
  { kind: "fileNames", setting: "agent-activity-file-names", hint: "Show what a chat is doing beside a file’s name, and highlight the name, in the tree and open files." },
];

export function EditorSettingsPanel() {
  const [ready, setReady] = createSignal(false);
  // Loaded null means probing is unavailable in this runtime.
  const [detectedEditors, setDetectedEditors] = createSignal<LoadState<FoundEditor[] | null>>(
    unloaded(),
  );
  createEffect(() => {
    if (ready()) return;
    void (async () => {
      await loadSharedAppState();
      syncEditorPrefsFromSnapshot();
      syncExternalOpenPrefsFromSnapshot();
      try {
        setDetectedEditors(loaded(hostSharesDevice() ? await detectEditors() : null));
      } catch (err) {
        setDetectedEditors(loadFailed<FoundEditor[] | null>(err));
      }
      setReady(true);
    })();
  });

  const prefs = () => externalOpenPrefs();
  const showSafari = () => tauriPlatform() === "macos";

  type EditorOption = {
    value: ExternalEditorPreset;
    label: string;
  };

  const editorOptions = createMemo<EditorOption[]>(() => {
    const detection = detectedEditors();
    const detected = valueOf(detection) ?? [];
    const saved = prefs().externalEditor;
    const opts: EditorOption[] = detected.map((e) => ({
      value: e.id,
      label: e.displayName,
    }));
    if (saved !== "custom" && !detected.some((e) => e.id === saved)) {
      // Successful probes distinguish absent editors from detection failures.
      const probed = isLoaded(detection) && detection.value !== null;
      const suffix = probed
        ? " — not detected"
        : errorOf(detection) !== undefined
          ? " — detection unavailable"
          : "";
      opts.push({ value: saved, label: `${EDITOR_LABELS[saved]}${suffix}` });
    }
    opts.push({ value: "custom", label: "Custom" });
    return opts;
  });

  const previewStyle = () => ({
    "font-family": cssFontFamilyForEditor(editorFontFamilyPref()),
    "font-size": `${editorFontSizePref()}px`,
    "line-height": String(editorLineHeightPref()),
  });

  const fontFamilyIsCustom = () => !editorFontIsGuaranteed(editorFontFamilyPref());

  // Unsaved custom values keep the field open.
  const [customFontOpen, setCustomFontOpen] = createSignal(fontFamilyIsCustom());
  const showCustomFont = () => customFontOpen() || fontFamilyIsCustom();
  const fontFamilySelectValue = () =>
    showCustomFont() ? "custom" : editorFontFamilyPref();

  // Preference updates leave the active draft intact.
  const [customFont, setCustomFont] = createSignal(
    fontFamilyIsCustom() ? editorFontFamilyPref() : "",
  );

  // Bundled fonts need no runtime probe.
  const fontAvailability = createMemo(() =>
    fontFamilyIsCustom() ? probeFontAvailability(editorFontFamilyPref()) : "available",
  );

  return (
    <div data-testid="editor-settings-panel">
      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Keyboard</h3>
        <div class="den-settings-pref-row" {...settingAnchor("editor-keymap")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("editor-keymap")}</span>
            <p class="den-settings-hint">Choose standard text editing or modal editing with normal, insert, and visual modes.</p>
          </div>
          <DenSelect aria-label={settingLabel("editor-keymap")} data-testid="editor-keymap" value={editorKeymapPref()}
            options={[{ value: "emacs", label: "Default" }, { value: "vim", label: "Vim" }]}
            onValueChange={value => { if (value === "emacs" || value === "vim") void saveEditorKeymap(value); }} />
        </div>
        <div class="den-settings-pref-row" {...settingAnchor("editor-tab-focus")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("editor-tab-focus")}</span>
            <p class="den-settings-hint">When off, Tab indents. Press Escape, then Tab to leave the editor, or F6 to move to the next region.</p>
          </div>
          <DenCheckbox aria-label={settingLabel("editor-tab-focus")} checked={editorTabMovesFocusPref()}
            onChange={event => void saveEditorTabMovesFocus(event.currentTarget.checked)} />
        </div>
      </div>
      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Text</h3>

        <div class="den-settings-pref-row" {...settingAnchor("editor-font-family")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("editor-font-family")}</span>
            <p
              class="den-settings-hint"
              style={previewStyle()}
              data-testid="editor-font-family-preview"
            >
              The quick brown fox jumps over the lazy dog
            </p>
          </div>
          <div class="den-settings-subsection">
            <DenSelect
              data-testid="editor-font-family"
              aria-label={settingLabel("editor-font-family")}
              value={fontFamilySelectValue()}
              disabled={!ready()}
              options={[
                ...DEN_EDITOR_FONT_FAMILY_OPTIONS.map((opt) => ({
                  value: opt.value,
                  label: opt.label,
                })),
                { value: "custom", label: "Custom…" },
              ]}
              onValueChange={(value) => {
                if (value === "custom") {
                  setCustomFontOpen(true);
                  const typed = sanitizeFontFamily(customFont());
                  if (typed) void saveEditorFontFamily(typed);
                  return;
                }
                setCustomFontOpen(false);
                void saveEditorFontFamily(value);
              }}
            />
            <Show when={showCustomFont()}>
              <DenInput
                data-testid="editor-font-family-custom"
                aria-label="Custom editor font family"
                value={customFont()}
                placeholder="e.g. Cascadia Code"
                onInput={(e) => setCustomFont(e.currentTarget.value)}
                onChange={(e) => {
                  const typed = sanitizeFontFamily(e.currentTarget.value);
                  if (typed) void saveEditorFontFamily(typed);
                }}
              />
            </Show>
          </div>
        </div>

        <Show when={fontAvailability() === "unavailable"}>
          <p
            class="den-settings-hint den-settings-pref-note"
            data-testid="editor-font-missing"
          >
            <strong>{editorFontFamilyPref()}</strong> is not installed on this
            machine, so the editor is rendering in the app font. Your choice is
            kept and returns on a machine that has it.
          </p>
        </Show>

        <div class="den-settings-pref-row" {...settingAnchor("editor-font-size")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("editor-font-size")}</span>
            <p class="den-settings-hint" style={previewStyle()} data-testid="editor-font-preview">
              The quick brown fox — {editorFontSizePref()}px
            </p>
          </div>
          <div class="den-settings-stepper" data-testid="editor-font-size">
            <DenButton
              variant="ghost"
              compact
              disabled={!ready() || editorFontSizePref() <= 10}
              data-testid="editor-font-size-dec"
              aria-label="Decrease font size"
              onClick={() => void saveEditorFontSize(editorFontSizePref() - 1)}
            >
              −
            </DenButton>
            <span class="den-settings-stepper__value">{editorFontSizePref()}</span>
            <DenButton
              variant="ghost"
              compact
              disabled={!ready() || editorFontSizePref() >= 20}
              data-testid="editor-font-size-inc"
              aria-label="Increase font size"
              onClick={() => void saveEditorFontSize(editorFontSizePref() + 1)}
            >
              +
            </DenButton>
          </div>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("editor-line-height")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("editor-line-height")}</span>
            <p class="den-settings-hint">
              Multiplier for the editor line height ({editorLineHeightPref().toFixed(2)}).
            </p>
          </div>
          <div class="den-settings-stepper" data-testid="editor-line-height">
            <DenButton
              variant="ghost"
              compact
              disabled={!ready() || editorLineHeightPref() <= 1.1}
              data-testid="editor-line-height-dec"
              aria-label="Decrease line height"
              onClick={() =>
                void saveEditorLineHeight(editorLineHeightPref() - 0.05)
              }
            >
              −
            </DenButton>
            <span class="den-settings-stepper__value">
              {editorLineHeightPref().toFixed(2)}
            </span>
            <DenButton
              variant="ghost"
              compact
              disabled={!ready() || editorLineHeightPref() >= 2.0}
              data-testid="editor-line-height-inc"
              aria-label="Increase line height"
              onClick={() =>
                void saveEditorLineHeight(editorLineHeightPref() + 0.05)
              }
            >
              +
            </DenButton>
          </div>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("reveal-in-tree")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("reveal-in-tree")}</span>
            <p class="den-settings-hint">Expand folders and scroll to files when you open or switch to them.</p>
          </div>
          <DenCheckbox checked={editorRevealInTreePref()} disabled={!ready()}
            data-testid="editor-reveal-in-tree"
            onChange={(e) => { void saveEditorRevealInTree(e.currentTarget.checked); }}>
            <span class="sr-only">{settingLabel("reveal-in-tree")}</span>
          </DenCheckbox>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("tree-visible-levels")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("tree-visible-levels")}</span>
            <p class="den-settings-hint">
              Limit pinned folder rows, including the root and ⋯. Deeper folders fold to reduce indentation.
              Short panes may show fewer levels. Off disables pinned rows and indentation folding.
            </p>
          </div>
          <DenSelect
            data-testid="editor-tree-visible-levels"
            aria-label={settingLabel("tree-visible-levels")}
            value={String(editorTreeVisibleLevelsPref())}
            disabled={!ready()}
            options={[
              { value: "0", label: "Off" },
              ...Array.from({ length: DEN_FILES_TREE_LEVELS.max - DEN_FILES_TREE_LEVELS.min + 1 }, (_, index) => {
                const levels = DEN_FILES_TREE_LEVELS.min + index;
                return { value: String(levels), label: `${levels} levels${levels === DEN_FILES_TREE_LEVELS.default ? " (default)" : ""}` };
              }),
            ]}
            onValueChange={value => { void saveEditorTreeVisibleLevels(Number(value)); }}
          />
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("editor-word-wrap")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("editor-word-wrap")}</span>
            <p class="den-settings-hint">
              Wrap long lines in the in-app file viewer instead of horizontal
              scroll.
            </p>
          </div>
          <DenCheckbox
            checked={editorWordWrapPref()}
            disabled={!ready()}
            data-testid="editor-word-wrap"
            onChange={(e) => {
              void saveEditorWordWrap(e.currentTarget.checked);
            }}
          >
            <span class="sr-only">{settingLabel("editor-word-wrap")}</span>
          </DenCheckbox>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("line-numbers")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("line-numbers")}</span>
            <p class="den-settings-hint">
              Show line numbers in the in-app file viewer.
            </p>
          </div>
          <DenCheckbox
            checked={editorLineNumbersPref()}
            disabled={!ready()}
            data-testid="editor-line-numbers"
            onChange={(e) => {
              void saveEditorLineNumbers(e.currentTarget.checked);
            }}
          >
            <span class="sr-only">{settingLabel("line-numbers")}</span>
          </DenCheckbox>
        </div>
      </div>

      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Version history</h3>

        <div class="den-settings-pref-row" {...settingAnchor("walk-controls-placement")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("walk-controls-placement")}</span>
            <p class="den-settings-hint">
              Dock above the editor status bar or float over the document.
              Moving the controls saves their location. Changing this default resets that location.
            </p>
          </div>
          <BrowseSegmented
            class="den-settings-segmented"
            testId="editor-walk-controls-default"
            ariaLabel={settingLabel("walk-controls-placement")}
            disabled={!ready()}
            value={editorWalkControlsDefaultPref()}
            onChange={id => {
              if (id === "floating" || id === "docked") void saveEditorWalkControlsDefault(id);
            }}
            options={[
              { id: "floating", label: "Floating", testId: "editor-walk-controls-floating" },
              { id: "docked", label: "Docked", testId: "editor-walk-controls-docked" },
            ]}
          />
        </div>
        <div class="den-settings-pref-row" {...settingAnchor("walk-controls-location")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("walk-controls-location")}</span>
            <p class="den-settings-hint">Return the controls to the default placement.</p>
          </div>
          <DenButton
            variant="ghost"
            disabled={!ready() || !editorWalkControlsLocationPref()}
            data-testid="editor-walk-controls-reset"
            onClick={() => { void saveEditorWalkControlsLocation(); }}
          >Reset location</DenButton>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("version-comparison")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("version-comparison")}</span>
            <p class="den-settings-hint">
              Current shows what restoring changes. Before shows what produced
              the selected version.
            </p>
          </div>
          <BrowseSegmented
            class="den-settings-segmented"
            testId="editor-version-comparison"
            ariaLabel="Default version comparison"
            disabled={!ready()}
            value={editorVersionComparisonPref()}
            onChange={(id) => {
              if (id === "current" || id === "before") {
                void saveEditorVersionComparison(id);
              }
            }}
            options={[
              {
                id: "current",
                label: "Current",
                testId: "editor-version-comparison-current",
              },
              {
                id: "before",
                label: "Before",
                testId: "editor-version-comparison-before",
              },
            ]}
          />
        </div>
      </div>

      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Indentation</h3>
        <div class="den-settings-pref-row">
          <p class="den-settings-hint">
            Used when a file&apos;s own indentation can&apos;t be detected.
          </p>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("indent-style")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("indent-style")}</span>
          </div>
          <BrowseSegmented
            class="den-settings-segmented"
            testId="editor-indent-style"
            ariaLabel="Indent style"
            disabled={!ready()}
            value={editorIndentStylePref()}
            onChange={(id) => {
              if (id === "spaces" || id === "tabs") {
                void saveEditorIndentStyle(id satisfies DenEditorIndentStyle);
              }
            }}
            options={[
              { id: "spaces", label: "Spaces", testId: "editor-indent-spaces" },
              { id: "tabs", label: "Tabs", testId: "editor-indent-tabs" },
            ]}
          />
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("indent-width")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("indent-width")}</span>
          </div>
          <BrowseSegmented
            class="den-settings-segmented"
            testId="editor-indent-width"
            ariaLabel="Indent width"
            disabled={!ready()}
            value={String(editorIndentWidthPref())}
            onChange={(id) => {
              const width = Number(id);
              if (width === 2 || width === 4 || width === 8) {
                void saveEditorIndentWidth(width);
              }
            }}
            options={[
              { id: "2", label: "2", testId: "editor-indent-width-2" },
              { id: "4", label: "4", testId: "editor-indent-width-4" },
              { id: "8", label: "8", testId: "editor-indent-width-8" },
            ]}
          />
        </div>
      </div>

      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Guides</h3>

        <div class="den-settings-pref-row" {...settingAnchor("indent-guides")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("indent-guides")}</span>
            <p class="den-settings-hint">Draw vertical guides at indent columns.</p>
          </div>
          <DenCheckbox
            checked={editorIndentGuidesPref()}
            disabled={!ready()}
            data-testid="editor-indent-guides"
            onChange={(e) => {
              void saveEditorIndentGuides(e.currentTarget.checked);
            }}
          >
            <span class="sr-only">{settingLabel("indent-guides")}</span>
          </DenCheckbox>
        </div>

        <div class="den-settings-pref-row" {...settingAnchor("show-whitespace")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("show-whitespace")}</span>
            <p class="den-settings-hint">
              Highlight spaces and tabs. Trailing whitespace is always marked
              while editing.
            </p>
          </div>
          <DenCheckbox
            checked={editorWhitespacePref()}
            disabled={!ready()}
            data-testid="editor-whitespace"
            onChange={(e) => {
              void saveEditorWhitespace(e.currentTarget.checked);
            }}
          >
            <span class="sr-only">{settingLabel("show-whitespace")}</span>
          </DenCheckbox>
        </div>
      </div>

      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Scrollbar ticks</h3>

        <div class="den-settings-pref-row" {...settingAnchor("scrollbar-ticks")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("scrollbar-ticks")}</span>
            <p class="den-settings-hint">
              Mark where symbols, changes, matches, the cursor, window selections, and agent activity
              sit in the whole file.
            </p>
          </div>
          <DenCheckbox
            checked={editorScrollbarTicksPref()}
            disabled={!ready()}
            data-testid="editor-scrollbar-ticks"
            onChange={(e) => {
              void saveEditorScrollbarTicks(e.currentTarget.checked);
            }}
          >
            <span class="sr-only">{settingLabel("scrollbar-ticks")}</span>
          </DenCheckbox>
        </div>

        <SettingsGovernedGroup
          label="Scrollbar tick kinds"
          active={editorScrollbarTicksPref()}
        >
          <For each={SCROLLBAR_TICK_ROWS}>
            {(row) => (
              <div class="den-settings-pref-row" {...settingAnchor(row.setting)}>
                <div class="den-settings-pref-copy">
                  <span class="den-settings-pref-label">{settingLabel(row.setting)}</span>
                  <p class="den-settings-hint">{row.hint}</p>
                </div>
                <DenCheckbox
                  checked={editorScrollbarTickKindsPref()[row.kind]}
                  disabled={!ready()}
                  data-testid={`editor-scrollbar-ticks-${row.kind}`}
                  onChange={(e) => {
                    void saveEditorScrollbarTickKind(
                      row.kind,
                      e.currentTarget.checked,
                    );
                  }}
                >
                  <span class="sr-only">{settingLabel(row.setting)}</span>
                </DenCheckbox>
              </div>
            )}
          </For>
        </SettingsGovernedGroup>
      </div>

      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Agent activity</h3>

        <div class="den-settings-pref-row" {...settingAnchor("agent-activity")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("agent-activity")}</span>
            <p class="den-settings-hint">
              Show what each chat and its workers read and are about to change, in muted agent
              colors. Reads and pending edits clear when the chat’s turn ends.
            </p>
          </div>
          <DenCheckbox
            checked={editorAgentActivityPref()}
            disabled={!ready()}
            data-testid="editor-agent-activity"
            onChange={(e) => {
              void saveEditorAgentActivity(e.currentTarget.checked);
            }}
          >
            <span class="sr-only">{settingLabel("agent-activity")}</span>
          </DenCheckbox>
        </div>

        <SettingsGovernedGroup
          label="Agent activity kinds"
          active={editorAgentActivityPref()}
        >
          <For each={AGENT_ACTIVITY_ROWS}>
            {(row) => (
              <div class="den-settings-pref-row" {...settingAnchor(row.setting)}>
                <div class="den-settings-pref-copy">
                  <span class="den-settings-pref-label">{settingLabel(row.setting)}</span>
                  <p class="den-settings-hint">{row.hint}</p>
                </div>
                <DenCheckbox
                  checked={editorAgentActivityKindsPref()[row.kind]}
                  disabled={!ready()}
                  data-testid={`editor-agent-activity-${row.kind}`}
                  onChange={(e) => {
                    void saveEditorAgentActivityKind(
                      row.kind,
                      e.currentTarget.checked,
                    );
                  }}
                >
                  <span class="sr-only">{settingLabel(row.setting)}</span>
                </DenCheckbox>
              </div>
            )}
          </For>
        </SettingsGovernedGroup>
      </div>

      <Show when={hostSharesDevice()}>
        <div class="den-settings-pref-group">
          <h3 class="den-settings-pref-group-title" {...chromeProps()}>Opening files</h3>

            <div class="den-settings-pref-row" {...settingAnchor("external-editor")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("external-editor")}</span>
                <p class="den-settings-hint">
                  Used by Open in for files and folders. Choose an
                  installed editor, or Custom to configure a command.
                </p>
              </div>
              <DenSelect
                data-testid="editor-external-editor"
                aria-label={settingLabel("external-editor")}
                value={prefs().externalEditor}
                disabled={!ready()}
                options={editorOptions().map((opt) => ({
                  value: opt.value,
                  label: opt.label,
                }))}
                onValueChange={(value) => {
                  const externalEditor = value as ExternalEditorPreset;
                  void saveExternalOpenPrefs({ externalEditor });
                }}
              />
            </div>
            <Show when={prefs().externalEditor === "custom"}>
              <div class="den-settings-pref-row">
                <div class="den-settings-pref-copy">
                  <span class="den-settings-pref-label">Custom open command</span>
                  <p class="den-settings-hint">
                    Use {"{path}"} and {"{line}"} tokens. Arguments are tokenized
                    — no shell.
                  </p>
                </div>
                <DenInput
                  data-testid="editor-custom-editor"
                  value={prefs().customOpenCommand ?? ""}
                  placeholder="cursor -g {path}:{line}"
                  onChange={(e) => {
                    void saveExternalOpenPrefs({
                      customOpenCommand: e.currentTarget.value,
                    });
                  }}
                />
              </div>
              <div class="den-settings-pref-row">
                <div class="den-settings-pref-copy">
                  <span class="den-settings-pref-label">Custom folder command</span>
                  <p class="den-settings-hint">
                    Use {"{path}"} for the folder. Leave blank to reuse the file
                    command when it has no {"{line}"} token.
                  </p>
                </div>
                <DenInput data-testid="editor-custom-folder" aria-label="Custom folder command" value={prefs().customOpenFolderCommand ?? ""}
                  placeholder="cursor {path}" onChange={(event) => {
                    void saveExternalOpenPrefs({ customOpenFolderCommand: event.currentTarget.value });
                  }} />
              </div>
            </Show>
        </div>
      </Show>

      <div class="den-settings-pref-group">
        <h3 class="den-settings-pref-group-title" {...chromeProps()}>Opening links</h3>

        <div class="den-settings-pref-row" {...settingAnchor("browser")}>
          <div class="den-settings-pref-copy">
            <span class="den-settings-pref-label">{settingLabel("browser")}</span>
            <p class="den-settings-hint">
              Used for web links and browser-compatible files in Open in.
              Web links ask before opening.
            </p>
          </div>
          <DenSelect
            data-testid="editor-browser"
            aria-label={settingLabel("browser")}
            value={prefs().browser}
            options={[
              { value: "system-default", label: "System default" },
              { value: "chrome", label: "Chrome" },
              { value: "firefox", label: "Firefox" },
              ...(showSafari() ? [{ value: "safari", label: "Safari" }] : []),
              { value: "custom", label: "Custom" },
            ]}
            onValueChange={(value) => {
              const browser = value as BrowserPreset;
              void saveExternalOpenPrefs({ browser });
            }}
          />
        </div>

        <Show when={prefs().browser === "custom"}>
          <div class="den-settings-pref-row">
            <div class="den-settings-pref-copy">
              <span class="den-settings-pref-label">Custom browser command</span>
              <p class="den-settings-hint">
                Use a {"{url}"} token. Arguments are tokenized — no shell.
              </p>
            </div>
            <DenInput
              data-testid="editor-custom-browser"
              value={prefs().customBrowserCommand ?? ""}
              placeholder="open -a Firefox {url}"
              onChange={(e) => {
                void saveExternalOpenPrefs({
                  customBrowserCommand: e.currentTarget.value,
                });
              }}
            />
          </div>
        </Show>
      </div>
    </div>
  );
}
