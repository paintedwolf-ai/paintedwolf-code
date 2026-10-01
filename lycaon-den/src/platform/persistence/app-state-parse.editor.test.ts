import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";
import { DEN_EDITOR_PREF_KEYS } from "../../settings/editor/editor-prefs.ts";

describe("parseAppState editor", () => {
  it.each([0, 3, 4, 5, 6, 7, 8, 9, 10])("accepts %i tree levels", treeVisibleLevels => {
    expect(parseAppState({ version: 1, recents: [], editor: { treeVisibleLevels } }).editor?.treeVisibleLevels).toBe(treeVisibleLevels);
  });
  it.each([-1, 1, 2, 11, 4.5, "5", null])("ignores invalid tree levels %s", treeVisibleLevels => {
    expect(parseAppState({ version: 1, recents: [], editor: { treeVisibleLevels } }).editor?.treeVisibleLevels).toBeUndefined();
  });

  it("round-trips every editor pref", () => {
    const editor = {
      keymap: "vim" as const,
      tabMovesFocus: true,
      wordWrap: true,
      revealInTree: false,
      treeVisibleLevels: 3,
      lineNumbers: false,
      fontSize: 14,
      indentGuides: false,
      whitespace: true,
      indentStyle: "tabs" as const,
      indentWidth: 8 as const,
      fontFamily: "SF Mono",
      lineHeight: 1.55,
      versionComparison: "before" as const,
      walkControlsDefault: "docked" as const,
      walkControlsLocation: { placement: "floating" as const, position: { x: 120, y: 75 } },
      scrollbarTicks: false,
      scrollbarTickKinds: {
        symbols: false,
        changes: true,
        matches: false,
        cursor: true,
        windows: false,
        agents: true,
      },
      agentActivity: false,
      agentActivityKinds: { reads: true, changes: false, fileNames: false },
    };
    expect(Object.keys(editor).sort()).toEqual([...DEN_EDITOR_PREF_KEYS].sort());
    const state = parseAppState({
      version: 1,
      recents: [],
      editor,
    });
    expect(state.editor).toEqual(editor);
  });

  it.each([
    { placement: "side" },
    { placement: "floating", position: { x: -1, y: 10 } },
    { placement: "floating", position: { x: Infinity, y: 10 } },
    { placement: "floating", position: { x: 10, y: "20" } },
    { placement: "floating", position: null },
  ])("rejects malformed walk locations: %j", walkControlsLocation => {
    expect(parseAppState({ version: 1, recents: [], editor: { walkControlsLocation } }).editor).toBeUndefined();
  });

  it.each([{ placement: "docked" }, { placement: "floating" }])("keeps walk placement without coordinates: %j", walkControlsLocation => {
    expect(parseAppState({ version: 1, recents: [], editor: { walkControlsLocation } }).editor?.walkControlsLocation).toEqual(walkControlsLocation);
  });

  it("drops non-boolean / out-of-range values", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      editor: {
        wordWrap: "yes",
        revealInTree: "off",
        lineNumbers: 1,
        fontSize: 99,
        indentStyle: "spaceship",
        indentWidth: 3,
        versionComparison: "parent",
        scrollbarTicks: "off",
        scrollbarTickKinds: { symbols: 0, matches: "no", windows: "yes" },
      },
    });
    expect(state.editor).toBeUndefined();
  });

  it("keeps known keys and drops unknown keys", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      editor: {
        wordWrap: true,
        revealInTree: false,
        mystery: true,
        fontSize: 14,
        scrollbarTickKinds: { matches: false, gutter: true },
      },
    });
    expect(state.editor).toEqual({
      wordWrap: true,
      revealInTree: false,
      fontSize: 14,
      scrollbarTickKinds: { matches: false },
    });
  });
});

it("keeps editing styles and Tab focus preferences, rejecting unknown styles", () => {
  for (const keymap of ["emacs", "vim"]) {
    const editor = parseAppState({ version: 1, recents: [], editor: { keymap, tabMovesFocus: true } }).editor;
    expect(editor).toMatchObject({ keymap, tabMovesFocus: true });
  }
  expect(parseAppState({ version: 1, recents: [], editor: { keymap: "unknown" } }).editor?.keymap).toBeUndefined();
});
