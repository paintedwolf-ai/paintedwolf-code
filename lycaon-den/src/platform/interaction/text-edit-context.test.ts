// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const host = vi.hoisted(() => ({
  platform: null as "macos" | null,
  invoke: vi.fn(async (_command: string) => undefined),
}));
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  tauriPlatform: () => host.platform,
}));
vi.mock("@tauri-apps/api/core", () => ({ invoke: host.invoke }));
import {
  copyEditable,
  cutEditable,
  editableHasSelection,
  findTextEditable,
  pasteEditable,
  resolveTextEditContext,
  selectAllAroundTarget,
  selectAllEditable,
  selectionCopyTextAtTarget,
  snapshotEditable,
} from "./text-edit-context.ts";

describe("text-edit-context", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.getSelection()?.removeAllRanges();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    window.getSelection()?.removeAllRanges();
  });

  it("finds textual inputs and textareas, skips file/button inputs", () => {
    const text = document.createElement("input");
    text.type = "text";
    const file = document.createElement("input");
    file.type = "file";
    const area = document.createElement("textarea");
    document.body.append(text, file, area);
    expect(findTextEditable(text)).toBe(text);
    expect(findTextEditable(file)).toBeNull();
    expect(findTextEditable(area)).toBe(area);
  });

  it("resolves editable context and selection-under-cursor Copy", () => {
    const input = document.createElement("input");
    input.value = "hello";
    document.body.appendChild(input);
    input.setSelectionRange(0, 2);
    const editableCtx = resolveTextEditContext(input);
    expect(editableCtx?.kind).toBe("editable");

    const prose = document.createElement("p");
    prose.style.userSelect = "text";
    prose.textContent = "selected copy";
    document.body.appendChild(prose);
    const range = document.createRange();
    range.selectNodeContents(prose);
    window.getSelection()?.addRange(range);
    expect(selectionCopyTextAtTarget(prose)).toBe("selected copy");
    expect(resolveTextEditContext(prose)?.kind).toBe("selection");
  });

  it("does not claim Copy when chrome is right-clicked over a live selection", () => {
    const prose = document.createElement("p");
    prose.style.userSelect = "text";
    prose.textContent = "selected";
    const chrome = document.createElement("div");
    chrome.style.userSelect = "none";
    chrome.textContent = "chrome";
    document.body.append(prose, chrome);
    const range = document.createRange();
    range.selectNodeContents(prose);
    window.getSelection()?.addRange(range);
    expect(selectionCopyTextAtTarget(chrome)).toBeNull();
    expect(resolveTextEditContext(chrome)).toBeNull();
  });

  it("claims Copy when the click is nested chrome inside the selected island", () => {
    const prose = document.createElement("p");
    prose.style.userSelect = "text";
    prose.append("before ");
    const chip = document.createElement("button");
    chip.style.userSelect = "none";
    chip.textContent = "path.ts";
    prose.append(chip, " after");
    document.body.appendChild(prose);
    const range = document.createRange();
    range.selectNodeContents(prose);
    window.getSelection()?.addRange(range);
    expect(selectionCopyTextAtTarget(chip)).toBe("before path.ts after");
    expect(resolveTextEditContext(chip)?.kind).toBe("selection");
  });

  it("does not claim Copy on a sibling selectable island", () => {
    const selected = document.createElement("p");
    selected.style.userSelect = "text";
    selected.textContent = "keep";
    const other = document.createElement("p");
    other.style.userSelect = "text";
    other.textContent = "other";
    document.body.append(selected, other);
    const range = document.createRange();
    range.selectNodeContents(selected);
    window.getSelection()?.addRange(range);
    expect(selectionCopyTextAtTarget(other)).toBeNull();
    expect(resolveTextEditContext(other)).toBeNull();
  });

  it("cuts, copies, pastes, and selects all on a textarea", async () => {
    const writeText = vi.fn(async () => undefined);
    const readText = vi.fn(async () => "ZZ");
    vi.stubGlobal("navigator", {
      clipboard: { writeText, readText },
    });

    const area = document.createElement("textarea");
    area.value = "abcdef";
    document.body.appendChild(area);
    area.setSelectionRange(2, 4);
    const snap = snapshotEditable(area);
    expect(editableHasSelection(snap)).toBe(true);

    await copyEditable(snap);
    expect(writeText).toHaveBeenCalledWith("cd");

    await cutEditable(snap);
    expect(area.value).toBe("abef");
    expect(writeText).toHaveBeenCalledWith("cd");

    const afterCut = snapshotEditable(area);
    await pasteEditable(afterCut);
    expect(readText).toHaveBeenCalled();
    expect(area.value).toContain("ZZ");

    selectAllEditable(snapshotEditable(area));
    expect(area.selectionStart).toBe(0);
    expect(area.selectionEnd).toBe(area.value.length);
  });

  it("honors a canceled beforeinput when a composer intercepts custom paste", async () => {
    vi.stubGlobal("navigator", {
      clipboard: { readText: vi.fn(async () => "large paste") },
    });
    const area = document.createElement("textarea");
    area.value = "keep";
    area.addEventListener("beforeinput", (event) => event.preventDefault());
    document.body.appendChild(area);
    area.setSelectionRange(4, 4);

    await pasteEditable(snapshotEditable(area));

    expect(area.value).toBe("keep");
  });

  it("pastes on macOS through the system paste into the restored selection", async () => {
    host.platform = "macos";
    try {
      const area = document.createElement("textarea");
      area.value = "abcdef";
      document.body.appendChild(area);
      area.setSelectionRange(1, 3);
      const snap = snapshotEditable(area);
      area.setSelectionRange(0, 0);
      area.blur();

      await pasteEditable(snap);

      expect(host.invoke).toHaveBeenCalledWith("paste_into_focused_view");
      expect(document.activeElement).toBe(area);
      expect([area.selectionStart, area.selectionEnd]).toEqual([1, 3]);
      expect(area.value).toBe("abcdef");
    } finally {
      host.platform = null;
    }
  });

  it("disables mutate paths on read-only inputs", async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", {
      clipboard: { writeText, readText: vi.fn(async () => "x") },
    });
    const input = document.createElement("input");
    input.value = "abc";
    input.readOnly = true;
    document.body.appendChild(input);
    input.setSelectionRange(0, 3);
    const snap = snapshotEditable(input);
    expect(snap.readOnly).toBe(true);
    await cutEditable(snap);
    expect(input.value).toBe("abc");
    await pasteEditable(snap);
    expect(input.value).toBe("abc");
  });

  it("selects the largest selectable ancestor", () => {
    const outer = document.createElement("div");
    outer.style.userSelect = "text";
    outer.textContent = "outer ";
    const inner = document.createElement("span");
    inner.style.userSelect = "text";
    inner.textContent = "inner";
    outer.appendChild(inner);
    document.body.appendChild(outer);
    selectAllAroundTarget(inner);
    expect(window.getSelection()?.toString()).toBe("outer inner");
  });
});
