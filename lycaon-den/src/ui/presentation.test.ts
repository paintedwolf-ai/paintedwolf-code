import { createRoot, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createPreparation, createPresentation, createPresentationIntent } from "./presentation.ts";

describe("presentation preparation", () => {
  it("composes declared dependencies and releases disposed children", () => createRoot((dispose) => {
    const preparation = createPreparation();
    const [ready, setReady] = createSignal(false);
    const first = preparation.register("tree", ready);
    const second = preparation.register("editor", () => false);
    expect(preparation.pending()).toEqual(["tree", "editor"]);
    setReady(true);
    expect(preparation.pending()).toEqual(["editor"]);
    second();
    expect(preparation.ready()).toBe(true);
    first();
    second();
    dispose();
  }));

  it("never confuses participants with the same diagnostic name", () => {
    const preparation = createPreparation();
    const first = preparation.register("body", () => false);
    const second = preparation.register("body", () => false);
    first();
    first();
    expect(preparation.pending()).toEqual(["body"]);
    second();
    expect(preparation.ready()).toBe(true);
  });
});

describe("presentation publication", () => {
  it("rejects a delayed layout completion after newer navigation", async () => {
    const navigation = createPresentationIntent();
    const layout = navigation.begin();
    const redirect = vi.fn();
    let finishSave!: () => void;
    const saved = new Promise<void>((resolve) => { finishSave = resolve; });
    const completion = saved.then(() => layout.commit(redirect));
    navigation.cancel();
    finishSave();
    expect(await completion).toBe(false);
    expect(redirect).not.toHaveBeenCalled();
  });

  it("invalidates superseded intents and every intent after disposal", () => {
    const navigation = createPresentationIntent();
    const first = navigation.begin();
    const second = navigation.begin();
    expect(first.current()).toBe(false);
    expect(second.current()).toBe(true);
    navigation.dispose();
    expect(second.current()).toBe(false);
    expect(navigation.begin().current()).toBe(false);
  });

  it("retains a complete display and rejects superseded A → B → A work", () => {
    const view = createPresentation<{ title: string; rows: string[] }>();
    view.begin().publish({ title: "A", rows: ["a1"] });
    const b = view.begin();
    expect(view.displayed()?.title).toBe("A");
    const a = view.begin();
    expect(b.publish({ title: "B", rows: ["b1"] })).toBe(false);
    a.publish({ title: "A refreshed", rows: ["a2"] });
    expect(view.displayed()).toEqual({ title: "A refreshed", rows: ["a2"] });
  });

  it("retains empty results and rejects cancelled or disposed work", () => {
    const view = createPresentation<string[]>();
    view.begin().publish([]);
    const cancelled = view.begin();
    view.cancel();
    expect(cancelled.publish(["cancelled"])).toBe(false);
    expect(view.displayed()).toEqual([]);
    const next = view.begin();
    view.dispose();
    expect(next.publish(["late"])).toBe(false);
  });

  it("confirms the displayed snapshot without replacing it, and only for the latest candidate", () => {
    const view = createPresentation<{ title: string }>();
    const first = { title: "A" };
    view.begin().publish(first);
    const stale = view.begin();
    const latest = view.begin();
    expect(stale.retain()).toBe(false);
    expect(latest.retain()).toBe(true);
    expect(view.displayed()).toBe(first);
  });

  it("publishes callable values without invoking them", () => {
    const value = vi.fn(() => "ready");
    const view = createPresentation<typeof value>();

    view.begin().publish(value);

    expect(view.displayed()).toBe(value);
    expect(value).not.toHaveBeenCalled();
  });
});
