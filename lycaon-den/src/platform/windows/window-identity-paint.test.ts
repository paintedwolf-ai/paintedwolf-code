// @vitest-environment jsdom
import { createSignal } from "solid-js";
import type { ItemWindowView } from "./item-windows.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { startWindowIdentityPaint } from "./window-identity-paint.ts";
import { windowColorProperties, readWindowPalette } from "../../contributions/window-color-palette.ts";

const identity = vi.hoisted(() => ({ clientId: "window:main", views: (): readonly ItemWindowView[] => [
  { label: "file:1", title: "shared.ts", kind: "file", viewNumber: 1, projectId: "project" },
] }));
vi.mock("../connection/client-identity.ts", () => ({ clientIdentity: () => identity.clientId }));
vi.mock("./item-windows.ts", () => ({ itemWindowViews: () => identity.views() }));
let stop: (() => void) | undefined;
afterEach(() => { stop?.(); stop = undefined; identity.clientId = "window:main"; document.documentElement.removeAttribute("style"); });

function setTheme(main: string, background: string, text: string) {
  const root = document.documentElement;
  for (const [property, value] of Object.entries({ "--den-background": background, "--den-text": text, "--den-accent-signal": main,
    ...windowColorProperties({ main, anchors: [main, "#8fbcbb"], hue_spread: 24, chroma_min: 0.04, chroma_max: 0.16 }) })) {
    root.style.setProperty(property, value);
  }
}

describe("active tab window identity", () => {
  it("gives a secondary native window its own tab, caret, and selection paint", async () => {
    setTheme("#aa5522", "#ffffff", "#222222");
    identity.clientId = "window:file:1";
    stop = startWindowIdentityPaint();
    const root = document.documentElement;
    const main = readWindowPalette(root)!(0), secondary = readWindowPalette(root)!(1);
    await vi.waitFor(() => expect(root.style.getPropertyValue("--den-current-window-caret")).toBe(secondary.caret));
    expect(secondary.caret).not.toBe(main.caret);
    expect(root.style.getPropertyValue("--den-current-window-selection")).toBe(secondary.selection);
    expect(root.style.getPropertyValue("--den-current-window-selection-strong")).toBe(secondary.selectionStrong);
    expect(secondary.selectionStrong).not.toBe(main.selectionStrong);
  });

  it("keeps the remaining window color when its peers close", async () => {
    const originalViews = identity.views;
    const [views, setViews] = createSignal<ItemWindowView[]>([1, 2, 9].map(slot => ({
      label: `file:${slot}`, title: "shared.ts", kind: "file", viewNumber: slot, projectId: "project",
    })));
    identity.views = views;
    identity.clientId = "window:file:9";
    try {
      setTheme("#d98a56", "#181818", "#eeeeee");
      stop = startWindowIdentityPaint();
      const root = document.documentElement;
      const color = readWindowPalette(root)!(9);
      await vi.waitFor(() => expect(root.style.getPropertyValue("--den-current-window-caret")).toBe(color.caret));
      setViews(views().filter(window => window.viewNumber === 9));
      await new Promise(resolve => setTimeout(resolve, 0));
      expect(root.style.getPropertyValue("--den-current-window-caret")).toBe(color.caret);
      expect(root.style.getPropertyValue("--den-current-window-selection-strong")).toBe(color.selectionStrong);
      expect(color.caret).not.toBe(readWindowPalette(root)!(0).caret);
    } finally { identity.views = originalViews; }
  });

  it("uses the main theme color and repaints after a theme change without changing identity", async () => {
    setTheme("#aa5522", "#ffffff", "#222222");
    stop = startWindowIdentityPaint();
    const root = document.documentElement;
    await vi.waitFor(() => expect(root.style.getPropertyValue("--den-current-window-caret")).toBe(readWindowPalette(root)?.(0).caret));
    const before = root.style.getPropertyValue("--den-current-window-caret");
    setTheme("#cc99dd", "#181818", "#eeeeee");
    await vi.waitFor(() => expect(root.style.getPropertyValue("--den-current-window-caret")).not.toBe(before));
    expect(root.style.getPropertyValue("--den-current-window-caret")).toBe(readWindowPalette(root)?.(0).caret);
    expect(root.style.getPropertyValue("--den-current-window-selection")).toBe(readWindowPalette(root)?.(0).selection);
    expect(root.style.getPropertyValue("--den-current-window-selection-strong")).toBe(readWindowPalette(root)?.(0).selectionStrong);
    stop(); stop = undefined;
    expect(root.style.getPropertyValue("--den-current-window-caret")).toBe("");
    expect(root.style.getPropertyValue("--den-current-window-selection")).toBe("");
    expect(root.style.getPropertyValue("--den-current-window-selection-strong")).toBe("");
  });
});
