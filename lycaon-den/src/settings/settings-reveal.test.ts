// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { REVEAL_FLASH_CLASS } from "../ui/reveal-flash.ts";
import { settingDefinition } from "./settings-registry.ts";
import {
  followSettingRevealTab,
  pendingSettingReveal,
  requestSettingReveal,
  revealSettingAnchor,
  settleSettingReveal,
} from "./settings-reveal.ts";

let root: HTMLElement;
const scrollIntoView = vi.fn();

beforeEach(() => {
  root = document.createElement("section");
  document.body.append(root);
  Element.prototype.scrollIntoView = scrollIntoView;
});

afterEach(() => {
  const request = pendingSettingReveal();
  if (request) settleSettingReveal(request);
  root.remove();
  scrollIntoView.mockClear();
});

function row(id: string, inner = ""): HTMLElement {
  const el = document.createElement("div");
  el.setAttribute("data-setting-id", id);
  el.innerHTML = inner;
  return el;
}

const always = () => true;

describe("revealSettingAnchor", () => {
  it("scrolls, flashes, and focuses the row's first enabled control", async () => {
    const anchor = row(
      "text-size",
      '<span>Text size</span><button disabled>Off</button><select><option>1</option></select>',
    );
    root.append(anchor);

    await expect(
      revealSettingAnchor(root, "text-size", { timeoutMs: 1000, isCurrent: always }),
    ).resolves.toBe(true);

    expect(scrollIntoView).toHaveBeenCalledTimes(1);
    expect(scrollIntoView.mock.contexts[0]).toBe(anchor);
    expect(anchor.classList.contains(REVEAL_FLASH_CLASS)).toBe(true);
    expect(document.activeElement).toBe(anchor.querySelector("select"));
  });

  it("prefers the control the row names over earlier ones", async () => {
    const anchor = row(
      "mcp-providers",
      '<button id="test">Test connection</button><button id="add" data-setting-control>Add provider</button>',
    );
    root.append(anchor);
    await revealSettingAnchor(root, "mcp-providers", { timeoutMs: 1000, isCurrent: always });
    expect(document.activeElement?.id).toBe("add");
  });

  it("focuses the row itself when its controls are disabled", async () => {
    const anchor = row("keep-awake", "<span>Keep awake</span><input type=checkbox disabled>");
    root.append(anchor);
    await revealSettingAnchor(root, "keep-awake", { timeoutMs: 1000, isCurrent: always });
    expect(document.activeElement).toBe(anchor);
    expect(anchor.tabIndex).toBe(-1);
  });

  it("waits for the row to mount and for hiding ancestors to publish", async () => {
    const surface = document.createElement("div");
    surface.setAttribute("aria-hidden", "true");
    root.append(surface);
    const pending = revealSettingAnchor(root, "line-numbers", {
      timeoutMs: 2000,
      isCurrent: always,
    });

    const anchor = row("line-numbers", "<input type=checkbox>");
    surface.append(anchor);
    await Promise.resolve();
    expect(anchor.classList.contains(REVEAL_FLASH_CLASS)).toBe(false);

    surface.removeAttribute("aria-hidden");
    await expect(pending).resolves.toBe(true);
    expect(anchor.classList.contains(REVEAL_FLASH_CLASS)).toBe(true);
  });

  it("skips a hidden copy in favor of the presented row", async () => {
    const hidden = document.createElement("div");
    hidden.hidden = true;
    hidden.append(row("browser"));
    const shown = row("browser", "<select></select>");
    root.append(hidden, shown);
    await revealSettingAnchor(root, "browser", { timeoutMs: 1000, isCurrent: always });
    expect(shown.classList.contains(REVEAL_FLASH_CLASS)).toBe(true);
  });

  it("gives up when the row never renders or the request is superseded", async () => {
    await expect(
      revealSettingAnchor(root, "privacy", { timeoutMs: 20, isCurrent: always }),
    ).resolves.toBe(false);

    let current = true;
    const pending = revealSettingAnchor(root, "privacy", {
      timeoutMs: 2000,
      isCurrent: () => current,
    });
    current = false;
    root.append(row("privacy"));
    await expect(pending).resolves.toBe(false);
    expect(scrollIntoView).not.toHaveBeenCalled();
  });
});

describe("setting reveal requests", () => {
  it("select the target tab in the panel of the requested section", () => {
    const [generalTab, setGeneralTab] = createSignal("display");
    const [debugTab, setDebugTab] = createSignal("budgets");
    const dispose = createRoot((dispose) => {
      followSettingRevealTab("general", setGeneralTab);
      followSettingRevealTab("debug", setDebugTab);
      return dispose;
    });

    const request = requestSettingReveal(settingDefinition("editor-font-size"));
    expect(generalTab()).toBe("editor");
    expect(debugTab()).toBe("budgets");

    settleSettingReveal(request);
    expect(pendingSettingReveal()).toBeNull();
    dispose();
  });

  it("settle only the request they name", () => {
    const first = requestSettingReveal(settingDefinition("appearance"));
    const second = requestSettingReveal(settingDefinition("browser"));
    settleSettingReveal(first);
    expect(pendingSettingReveal()).toBe(second);
  });

  it("expire when no Settings view lands them", () => {
    vi.useFakeTimers();
    try {
      requestSettingReveal(settingDefinition("appearance"));
      vi.advanceTimersByTime(10_000);
      expect(pendingSettingReveal()).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});
