import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { keymap } from "@codemirror/view";
import { EditorState } from "@codemirror/state";
import { buildEditorCommandBridge, denChordToCodeMirror } from "./editor-command-bridge.ts";
import { EDITOR_EDITING_COMMAND_IDS } from "./editor-commands.ts";
import { seedStockFrame } from "../../../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../../../contributions/contribution-store.ts";
import { liveKeymapSource } from "../../../contributions/frame-keymap.ts";
import { nativeCommandId } from "../../../contributions/dispatch.ts";
import { resolveKeymap } from "../../../shortcuts/keymap.ts";
import { setShortcutPlatformForTests } from "../../../shortcuts/platform.ts";
import { denSrc } from "../../../shell/den-client-state-invariants.ts";
import { readSourceText } from "../../../test/stylesheet-source.ts";
import {
  registerCommandHandler,
  resetDispatcherForTests,
  setFilesStageActive,
} from "../../../shortcuts/dispatcher.ts";

const FINDING_NAVIGATION_COMMAND_IDS = ["editor.nextFinding", "editor.previousFinding"] as const;

function bridgeBindings() {
  const state = EditorState.create({
    extensions: [buildEditorCommandBridge("macos", null)],
  });
  return state.facet(keymap).flat();
}

function bridgeKeys(): string[] {
  return bridgeBindings().flatMap((binding) =>
    binding.key ? [binding.key] : [],
  );
}

beforeEach(() => {
  setShortcutPlatformForTests("macos");
  seedStockFrame();
});

afterEach(() => {
  resetDispatcherForTests();
  resetContributionStoreForTest();
  setShortcutPlatformForTests(null);
});

describe("buildEditorCommandBridge", () => {
  it("binds the stock multi-cursor chord", () => {
    expect(bridgeKeys()).toContain("Ctrl-Shift-ArrowUp");
  });

  it("binds findings navigation", () => {
    const keys = bridgeKeys();
    expect(keys).toContain("F8");
    expect(keys).toContain("Shift-F8");
  });

  it("routes each single-chord declaration to its editor handler", () => {
    setFilesStageActive(true);
    const resolved = resolveKeymap(null, liveKeymapSource("macos"));
    const bridge = bridgeBindings();
    const invoked: string[] = [];
    let singleChordCount = 0;
    for (const id of [...EDITOR_EDITING_COMMAND_IDS, ...FINDING_NAVIGATION_COMMAND_IDS]) {
      registerCommandHandler(id, () => { invoked.push(id); });
      const commandId = nativeCommandId(id);
      const singles = resolved.bindings.filter((row) => row.commandId === commandId);
      const sequences = resolved.sequences.filter((row) => row.commandId === commandId);
      expect(singles.length + sequences.length, `${id} has a binding`).toBeGreaterThan(0);
      for (const single of singles) {
        invoked.length = 0;
        const binding = bridge.find((row) => row.key === denChordToCodeMirror(single.chord));
        expect(binding?.run?.({ dom: null } as never), id).toBe(true);
        expect(invoked, single.chord).toEqual([id]);
        singleChordCount++;
      }
    }
    expect(bridge.length).toBeGreaterThanOrEqual(singleChordCount);
  });

  it("binds nothing when no frame is hydrated", () => {
    resetContributionStoreForTest();
    expect(bridgeKeys()).toEqual([]);
  });

  it("checks declaration scope when a binding runs", () => {
    const binding = bridgeBindings().find((row) => row.key === "F8");
    const handler = vi.fn();
    registerCommandHandler("editor.nextFinding", handler);

    setFilesStageActive(false);
    expect(binding?.run?.({ dom: null } as never)).toBe(false);
    setFilesStageActive(true);
    expect(binding?.run?.({ dom: null } as never)).toBe(true);
    expect(handler).toHaveBeenCalledOnce();
  });
});

describe("editor keymap routing", () => {
  it("names no chord of its own", () => {
    // Key literals here bypass user rebinding.
    const theme = readSourceText(join(denSrc, "components/source/editor/codemirror-theme.ts"));
    expect(theme).not.toMatch(/\{\s*key:\s*"/);
  });
});
