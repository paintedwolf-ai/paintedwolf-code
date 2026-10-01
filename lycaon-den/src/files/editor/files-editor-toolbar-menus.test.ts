import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { seedStockFrame } from "../../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import { contributionCommand, nativeCommandId } from "../../contributions/dispatch.ts";
import {
  filesEditorFindMenuItems,
  filesEditorGotoMenuItems,
} from "./files-editor-toolbar-menus.ts";

beforeEach(() => seedStockFrame());
afterEach(resetContributionStoreForTest);

describe("filesEditorFindMenuItems", () => {
  it("lists in-view find, replace, next/prev, select-all, and everywhere", () => {
    const actions = {
      findInFile: vi.fn(),
      replaceInFile: vi.fn(),
      findNext: vi.fn(),
      findPrev: vi.fn(),
      selectAllMatches: vi.fn(),
      findEverywhere: vi.fn(),
    };
    const items = filesEditorFindMenuItems(actions);
    expect(items.map((i) => i.testId)).toEqual([
      "files-editor-find-in-file",
      "files-editor-find-replace",
      "files-editor-find-next",
      "files-editor-find-prev",
      "files-editor-find-select-all",
      "files-editor-find-everywhere",
    ]);
    expect(items[0]?.label).toBe(contributionCommand(nativeCommandId("find.inView")!)?.title);
    expect(items[1]?.label).toBe(contributionCommand(nativeCommandId("find.replace")!)?.title);
    expect(items[5]?.label?.startsWith("Find everywhere")).toBe(true);
    items[2]?.onSelect!();
    expect(actions.findNext).toHaveBeenCalledTimes(1);
  });
});

describe("filesEditorGotoMenuItems", () => {
  it("lists line, definition, and finding navigation", () => {
    const actions = {
      goToLine: vi.fn(),
      goToDefinition: vi.fn(),
      nextFinding: vi.fn(),
      previousFinding: vi.fn(),
      showSecretScreen: vi.fn(),
    };
    const items = filesEditorGotoMenuItems(actions);
    expect(items.map((i) => i.testId)).toEqual([
      "files-editor-goto-line",
      "files-editor-goto-definition",
      "files-editor-show-secret-screen",
      "files-editor-goto-next-finding",
      "files-editor-goto-prev-finding",
    ]);
    expect(items[0]?.label?.startsWith("Go to line")).toBe(true);
    items[2]?.onSelect!();
    expect(actions.showSecretScreen).toHaveBeenCalledTimes(1);
    items[3]?.onSelect!();
    expect(actions.nextFinding).toHaveBeenCalledTimes(1);
  });
});
