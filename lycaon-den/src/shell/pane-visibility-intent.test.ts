import { afterEach, describe, expect, it } from "vitest";
import { createPaneVisibilityIntent } from "./pane-visibility-intent.ts";
import { resetShellLayoutBusyForTests } from "./shell-layout-busy.ts";

function fixture(initiallyCollapsed: boolean) {
  let collapsed = initiallyCollapsed;
  const widens: Array<() => void> = [];
  const paints: string[] = [];
  const intent = createPaneVisibilityIntent({
    collapsed: () => collapsed,
    widen: () => new Promise<void>((resolve) => widens.push(resolve)),
    paintOpen: () => {
      paints.push("open");
      collapsed = false;
    },
    paintClosed: () => {
      paints.push("closed");
      collapsed = true;
    },
  });
  return { intent, widens, paints, collapsed: () => collapsed };
}

describe("pane visibility intent", () => {
  afterEach(resetShellLayoutBusyForTests);

  it("paints the pane open only after the window widens", async () => {
    const f = fixture(true);
    const opened = f.intent.show();
    expect(f.intent.intendsOpen()).toBe(true);
    expect(f.paints).toEqual([]);
    f.widens[0]!();
    await expect(opened).resolves.toBe(true);
    expect(f.paints).toEqual(["open"]);
    expect(f.collapsed()).toBe(false);
  });

  it("a second toggle during the resize reads the request and closes the pane", async () => {
    const f = fixture(true);
    f.intent.toggle();
    f.intent.toggle();
    expect(f.intent.intendsOpen()).toBe(false);
    f.widens[0]!();
    await Promise.resolve();
    await Promise.resolve();
    expect(f.paints).toEqual(["closed"]);
    expect(f.collapsed()).toBe(true);
  });

  it("a third toggle starts a fresh restore that wins", async () => {
    const f = fixture(true);
    f.intent.toggle();
    f.intent.toggle();
    f.intent.toggle();
    expect(f.intent.intendsOpen()).toBe(true);
    for (const widen of f.widens) widen();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(f.paints).toEqual(["closed", "open"]);
    expect(f.collapsed()).toBe(false);
  });

  it("reports a restore overtaken by a hide so focus stays put", async () => {
    const f = fixture(true);
    const opened = f.intent.show();
    f.intent.hide();
    f.widens[0]!();
    await expect(opened).resolves.toBe(false);
  });

  it("joins repeat show requests into one resize", () => {
    const f = fixture(true);
    const a = f.intent.show();
    const b = f.intent.show();
    expect(a).toBe(b);
    expect(f.widens).toHaveLength(1);
  });
});
