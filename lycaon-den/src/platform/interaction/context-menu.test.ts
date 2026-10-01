// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  setupContextMenuGuard,
  teardownContextMenuGuard,
} from "./context-menu.ts";

describe("context-menu guard", () => {
  beforeEach(() => {
    teardownContextMenuGuard();
    vi.restoreAllMocks();
  });

  afterEach(() => {
    teardownContextMenuGuard();
    document.body.innerHTML = "";
  });

  it("always preventDefaults in prod (including over editables and selection)", () => {
    setupContextMenuGuard();

    const input = document.createElement("input");
    document.body.appendChild(input);
    const onEditable = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
    });
    input.dispatchEvent(onEditable);
    expect(onEditable.defaultPrevented).toBe(true);

    const text = document.createElement("div");
    text.style.userSelect = "text";
    text.textContent = "selectable";
    document.body.appendChild(text);
    const range = document.createRange();
    range.selectNodeContents(text);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);
    const onSelection = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
    });
    text.dispatchEvent(onSelection);
    expect(onSelection.defaultPrevented).toBe(true);
  });

  it("suppresses the native menu over non-selectable chrome", () => {
    setupContextMenuGuard();
    const chrome = document.createElement("div");
    chrome.style.userSelect = "none";
    document.body.appendChild(chrome);
    const suppressed = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
    });
    chrome.dispatchEvent(suppressed);
    expect(suppressed.defaultPrevented).toBe(true);
  });
});
