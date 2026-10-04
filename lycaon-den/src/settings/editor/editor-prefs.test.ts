import { beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  resetAppStateSnapshotForTests,
  getAppStateSnapshot,
  flushAppState,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import {
  DEN_EDITOR_PREF_KEYS,
  editorTreeVisibleLevelsPref,
  resolveEditorTreeVisibleLevels,
  saveEditorTreeVisibleLevels,
  editorWalkControlsPlacement,
  editorWalkControlsDefaultPref,
  saveEditorWalkControlsDefault,
  saveEditorWalkControlsLocation,
  cssFontFamilyForEditor,
  editorAgentActivityKindsPref,
  editorAgentActivityPref,
  editorShownAgentActivity,
  resolveEditorAgentActivity,
  resolveEditorAgentActivityKinds,
  saveEditorAgentActivity,
  saveEditorAgentActivityKind,
  editorFontFamilyPref,
  editorFontSizePref,
  editorIndentGuidesPref,
  editorIndentStylePref,
  editorIndentWidthPref,
  editorLineHeightPref,
  editorLineNumbersPref,
  editorScrollbarTickKindsPref,
  editorScrollbarTicksPref,
  editorShownScrollbarTicks,
  editorWhitespacePref,
  editorWordWrapPref,
  editorRevealInTreePref,
  editorVersionComparisonPref,
  resetEditorPrefsForTests,
  resolveEditorFontFamily,
  resolveEditorFontSize,
  resolveEditorIndentGuides,
  resolveEditorIndentStyle,
  resolveEditorIndentWidth,
  resolveEditorLineHeight,
  resolveEditorLineNumbers,
  resolveEditorScrollbarTickKinds,
  resolveEditorScrollbarTicks,
  resolveEditorWhitespace,
  resolveEditorWordWrap,
  resolveEditorVersionComparison,
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
  defaultEditorKeymap,
  editorKeymapPref,
  resolveEditorKeymap,
} from "./editor-prefs.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";

vi.mock("../../platform/persistence/app-state.ts", () => ({
  loadAppState: vi.fn().mockResolvedValue({ ...EMPTY_APP_STATE_V1 }),
  patchAppState: vi.fn(async (_patch, fallback) => fallback),
}));

describe("editor-prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetEditorPrefsForTests();
  });

  it.each([0, 3, 4, 5, 6, 7, 8, 9, 10])("persists and restores %i visible tree levels", async treeVisibleLevels => {
    expect(editorTreeVisibleLevelsPref()).toBe(5);
    const saving = saveEditorTreeVisibleLevels(treeVisibleLevels);
    expect(editorTreeVisibleLevelsPref()).toBe(treeVisibleLevels);
    await flushAppState();
    await saving;
    const persisted = getAppStateSnapshot();
    expect(persisted.editor?.treeVisibleLevels).toBe(treeVisibleLevels);
    resetEditorPrefsForTests();
    setAppStateSnapshot(persisted);
    syncEditorPrefsFromSnapshot();
    expect(editorTreeVisibleLevelsPref()).toBe(treeVisibleLevels);
  });

  it.each([undefined, -1, 1, 2, 11, 4.5, NaN, Infinity])("defaults invalid tree levels %s to five", treeVisibleLevels => {
    expect(resolveEditorTreeVisibleLevels({ treeVisibleLevels })).toBe(5);
  });

  it("remembers a location across reload and resets it when the default changes", async () => {
    expect(editorWalkControlsPlacement()).toEqual({ placement: "floating" });
    const location = { placement: "floating" as const, position: { x: 125, y: 240 } };
    const saving = saveEditorWalkControlsLocation(location);
    await flushAppState();
    await saving;
    const persisted = getAppStateSnapshot();
    resetEditorPrefsForTests();
    setAppStateSnapshot(persisted);
    syncEditorPrefsFromSnapshot();
    expect(editorWalkControlsPlacement()).toEqual(location);
    const changing = saveEditorWalkControlsDefault("docked");
    await flushAppState();
    await changing;
    expect(editorWalkControlsDefaultPref()).toBe("docked");
    expect(editorWalkControlsPlacement()).toEqual({ placement: "docked" });
    expect(getAppStateSnapshot().editor?.walkControlsLocation).toBeUndefined();
    const moving = saveEditorWalkControlsLocation(location);
    await flushAppState();
    await moving;
    const resetting = saveEditorWalkControlsLocation();
    await flushAppState();
    await resetting;
    expect(editorWalkControlsPlacement()).toEqual({ placement: "docked" });
  });

  it("exposes the persisted editor preference keys", () => {
    expect([...DEN_EDITOR_PREF_KEYS].sort()).toEqual(
      [
        "keymap",
        "tabMovesFocus",
        "fontFamily",
        "fontSize",
        "indentGuides",
        "indentStyle",
        "indentWidth",
        "lineHeight",
        "lineNumbers",
        "whitespace",
        "wordWrap",
        "revealInTree",
        "treeVisibleLevels",
        "versionComparison",
        "walkControlsDefault",
        "walkControlsLocation",
        "scrollbarTicks",
        "scrollbarTickKinds",
        "agentActivity",
        "agentActivityKinds",
      ].sort(),
    );
  });

  it("defaults match the product SSOT", () => {
    expect(resolveEditorWordWrap(undefined)).toBe(false);
    expect(resolveEditorLineNumbers(undefined)).toBe(true);
    expect(resolveEditorFontSize(undefined)).toBe(13);
    expect(resolveEditorIndentGuides(undefined)).toBe(true);
    expect(resolveEditorWhitespace(undefined)).toBe(false);
    expect(resolveEditorIndentStyle(undefined)).toBe("spaces");
    expect(resolveEditorIndentWidth(undefined)).toBe(4);
    expect(resolveEditorFontFamily(undefined)).toBe("default");
    expect(resolveEditorLineHeight(undefined)).toBe(1.5);
    expect(resolveEditorVersionComparison(undefined)).toBe("current");
    expect(resolveEditorScrollbarTicks(undefined)).toBe(true);
    expect(resolveEditorScrollbarTickKinds(undefined)).toEqual({
      symbols: true,
      changes: true,
      matches: true,
      cursor: true,
      windows: true,
      agents: true,
    });
  });

  it("resolves an unset editing style by platform and keeps a chosen one everywhere", () => {
    expect(defaultEditorKeymap("macos")).toBe("emacs");
    expect(defaultEditorKeymap("linux")).toBe("standard");
    expect(defaultEditorKeymap("windows")).toBe("standard");
    for (const platform of ["macos", "linux", "windows"] as const) {
      expect(resolveEditorKeymap(undefined, platform)).toBe(defaultEditorKeymap(platform));
      expect(resolveEditorKeymap({}, platform)).toBe(defaultEditorKeymap(platform));
      for (const keymap of ["standard", "emacs", "vim"] as const) {
        expect(resolveEditorKeymap({ keymap }, platform)).toBe(keymap);
      }
    }
    setShortcutPlatformForTests("linux");
    try {
      resetEditorPrefsForTests();
      expect(editorKeymapPref()).toBe("standard");
    } finally {
      setShortcutPlatformForTests(null);
      resetEditorPrefsForTests();
    }
  });

  it("hides every tick kind behind the master switch and keeps the picks", async () => {
    await saveEditorScrollbarTickKind("matches", false);
    await saveEditorScrollbarTicks(false);
    expect(editorShownScrollbarTicks()).toEqual({
      symbols: false,
      changes: false,
      matches: false,
      cursor: false,
      windows: false,
      agents: false,
    });
    await saveEditorScrollbarTicks(true);
    expect(editorShownScrollbarTicks()).toEqual({
      symbols: true,
      changes: true,
      matches: false,
      cursor: true,
      windows: true,
      agents: true,
    });
  });

  it("shows agent activity by default and keeps kind picks behind the master switch", async () => {
    expect(resolveEditorAgentActivity(undefined)).toBe(true);
    expect(resolveEditorAgentActivityKinds(undefined)).toEqual({ reads: true, changes: true, fileNames: true });
    await saveEditorAgentActivityKind("changes", false);
    await saveEditorAgentActivity(false);
    expect(editorShownAgentActivity()).toEqual({ reads: false, changes: false, fileNames: false });
    await saveEditorAgentActivity(true);
    expect(editorShownAgentActivity()).toEqual({ reads: true, changes: false, fileNames: true });
    syncEditorPrefsFromSnapshot();
    expect(editorAgentActivityKindsPref().changes).toBe(false);
    expect(editorAgentActivityPref()).toBe(true);
  });

  it("falls back unknown font families at render", () => {
    expect(cssFontFamilyForEditor("default")).toBe("var(--den-font-mono)");
    expect(cssFontFamilyForEditor("NoSuchFont")).toContain("NoSuchFont");
    expect(cssFontFamilyForEditor("NoSuchFont")).toContain(
      "var(--den-font-mono)",
    );
  });

  it("defaults to revealing files and preserves an explicit opt-out", async () => {
    expect(editorRevealInTreePref()).toBe(true);
    await saveEditorRevealInTree(false);
    syncEditorPrefsFromSnapshot();
    expect(editorRevealInTreePref()).toBe(false);
    resetEditorPrefsForTests();
    expect(editorRevealInTreePref()).toBe(true);
  });

  it("syncs and saves every key", async () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      editor: {
        wordWrap: true,
        lineNumbers: false,
        fontSize: 15,
        indentGuides: false,
        whitespace: true,
        indentStyle: "tabs",
        indentWidth: 2,
        fontFamily: "Menlo",
        lineHeight: 1.75,
        versionComparison: "before",
        scrollbarTicks: false,
        scrollbarTickKinds: { changes: false },
      },
    });
    syncEditorPrefsFromSnapshot();
    expect(editorScrollbarTicksPref()).toBe(false);
    expect(editorScrollbarTickKindsPref()).toEqual({
      symbols: true,
      changes: false,
      matches: true,
      cursor: true,
      windows: true,
      agents: true,
    });
    expect(editorWordWrapPref()).toBe(true);
    expect(editorLineNumbersPref()).toBe(false);
    expect(editorFontSizePref()).toBe(15);
    expect(editorIndentGuidesPref()).toBe(false);
    expect(editorWhitespacePref()).toBe(true);
    expect(editorIndentStylePref()).toBe("tabs");
    expect(editorIndentWidthPref()).toBe(2);
    expect(editorFontFamilyPref()).toBe("Menlo");
    expect(editorLineHeightPref()).toBe(1.75);
    expect(editorVersionComparisonPref()).toBe("before");

    await saveEditorWordWrap(false);
    await saveEditorLineNumbers(true);
    await saveEditorFontSize(12);
    await saveEditorIndentGuides(true);
    await saveEditorWhitespace(false);
    await saveEditorIndentStyle("spaces");
    await saveEditorIndentWidth(8);
    await saveEditorFontFamily("JetBrains Mono");
    await saveEditorLineHeight(1.2);
    await saveEditorVersionComparison("current");
    await saveEditorScrollbarTicks(true);
    await saveEditorScrollbarTickKind("changes", true);

    expect(editorScrollbarTicksPref()).toBe(true);
    expect(editorScrollbarTickKindsPref().changes).toBe(true);
    expect(editorWordWrapPref()).toBe(false);
    expect(editorLineNumbersPref()).toBe(true);
    expect(editorFontSizePref()).toBe(12);
    expect(editorIndentGuidesPref()).toBe(true);
    expect(editorWhitespacePref()).toBe(false);
    expect(editorIndentStylePref()).toBe("spaces");
    expect(editorIndentWidthPref()).toBe(8);
    expect(editorFontFamilyPref()).toBe("JetBrains Mono");
    expect(editorLineHeightPref()).toBe(1.2);
    expect(editorVersionComparisonPref()).toBe("current");
  });
});
