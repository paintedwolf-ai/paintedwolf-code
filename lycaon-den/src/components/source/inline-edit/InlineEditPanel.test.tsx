import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { stockBindingId } from "../../../contributions/stock-frame-test.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  inlineEditInstruction,
  inlineEditOpen,
  inlineEditRenameInFileCount,
  inlineEditRenameProjectCount,
  inlineEditRenameScope,
  openInlineEdit,
  resetInlineEditForTests,
  setInlineEditInstruction,
  setInlineEditRenameCounts,
} from "./inline-edit-controller.ts";
import { InlineEditPanel } from "./InlineEditPanel.tsx";
import { setShortcutPlatformForTests } from "../../../shortcuts/platform.ts";
import {
  resetShortcutPrefsForTests,
  saveShortcutOverride,
} from "../../../settings/system/shortcut-prefs.ts";

function openRename(): void {
  openInlineEdit({
    projectId: "p1",
    rootId: "r1",
    path: "src/a.ts",
    bufferKey: "r1:src/a.ts",
    scope: { startLine: 1, endLine: 1, kind: "symbol", symbolName: "oldName" },
    mode: { kind: "rename", symbolName: "oldName" },
    anchor: { top: 80, left: 20, width: 360 },
  });
}

function renderPanel() {
  const mount = document.createElement("div");
  document.body.appendChild(mount);
  const result = render(() => (
    <InlineEditPanel
      mountEl={mount}
      docText={() => "const oldName = 1;"}
      onSubmit={vi.fn()}
    />
  ));
  return { ...result, mount };
}

beforeEach(() => {
  resetInlineEditForTests();
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests("macos");
});

afterEach(() => {
  cleanup();
  resetInlineEditForTests();
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests(null);
});

describe("InlineEditPanel shortcut projection", () => {
  it("keeps repeated rename opens for the same target idempotent", () => {
    openRename();
    const original = inlineEditOpen();
    setInlineEditInstruction("newName");
    setInlineEditRenameCounts({
      inFile: 4,
      project: { matches: 9, files: 3, truncated: false },
      pending: false,
    });

    expect(
      openInlineEdit({
        ...original!,
        anchor: { top: 120, left: 40, width: 420 },
      }),
    ).toBe(false);
    expect(inlineEditOpen()).toBe(original);
    expect(inlineEditInstruction()).toBe("newName");
    expect(inlineEditRenameInFileCount()).toBe(4);
    expect(inlineEditRenameProjectCount()).toEqual({
      matches: 9,
      files: 3,
      truncated: false,
    });
  });

  it("retargets rename when F2 resolves a different symbol scope", () => {
    openRename();
    const original = inlineEditOpen()!;
    setInlineEditInstruction("newName");

    expect(
      openInlineEdit({
        ...original,
        scope: {
          startLine: 8,
          endLine: 12,
          kind: "symbol",
          symbolName: "otherName",
        },
        mode: { kind: "rename", symbolName: "otherName" },
      }),
    ).toBe(true);
    expect(inlineEditOpen()?.mode).toEqual({
      kind: "rename",
      symbolName: "otherName",
    });
    expect(inlineEditInstruction()).toBe("");
  });

  it("shows Option glyphs on macOS and uses directional scope bindings", () => {
    openRename();
    const { mount } = renderPanel();

    expect(screen.getByTestId("inline-edit-panel").textContent).toContain(
      "⌥← / ⌥→ scope",
    );
    const input = screen.getByTestId("inline-edit-input");
    fireEvent.keyDown(input, {
      key: "ArrowRight",
      code: "ArrowRight",
      altKey: true,
    });
    expect(inlineEditRenameScope()).toBe("everywhere");
    fireEvent.keyDown(input, {
      key: "ArrowLeft",
      code: "ArrowLeft",
      altKey: true,
    });
    expect(inlineEditRenameScope()).toBe("file");
    mount.remove();
  });

  it("updates the opener, hint, and handler after a rebind", async () => {
    await saveShortcutOverride(stockBindingId("editor-rename-symbol"), "Mod+R");
    await saveShortcutOverride(
      stockBindingId("editor-rename-scope-everywhere"),
      "Mod+E",
    );
    openRename();
    const { mount } = renderPanel();

    const panel = screen.getByTestId("inline-edit-panel");
    expect(panel.textContent).toContain("⌘R");
    expect(panel.textContent).toContain("⌘E scope");

    fireEvent.keyDown(screen.getByTestId("inline-edit-input"), {
      key: "e",
      code: "KeyE",
      metaKey: true,
    });
    expect(inlineEditRenameScope()).toBe("everywhere");
    mount.remove();
  });
});
