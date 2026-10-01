import { describe, expect, it, vi } from "vitest";
import type { SplitPane } from "../../shared/app-state-types.ts";
import { createSplitPaneVisibility } from "./split-pane-visibility.ts";

function fixture(initial: SplitPane | null = null) {
  let hidden = initial;
  let finish!: () => void;
  const onHide = vi.fn();
  const api = createSplitPaneVisibility({
    hidden: () => hidden,
    commit: pane => { hidden = pane; },
    widen: () => new Promise<void>(resolve => { finish = resolve; }),
    onHide,
  });
  return { ...api, hidden: () => hidden, finish: () => finish(), onHide };
}

describe("split pane visibility", () => {
  it("hiding either pane reveals the other atomically", () => {
    const f = fixture();
    f.hide("stage");
    expect(f.hidden()).toBe("stage");
    f.hide("conversation");
    expect(f.hidden()).toBe("conversation");
    expect(f.onHide).toHaveBeenLastCalledWith("conversation");
  });

  it("coalesces restoring a hidden pane and does not reveal the other pane", async () => {
    const f = fixture("stage");
    expect(await f.show("conversation")).toBe(true);
    expect(f.hidden()).toBe("stage");
    const first = f.show("stage");
    expect(f.show("stage")).toBe(first);
    f.finish();
    expect(await first).toBe(true);
    expect(f.hidden()).toBeNull();
  });

  it("a later hide cancels an earlier restore across both panes", async () => {
    const f = fixture("stage");
    const restoring = f.show("stage");
    f.hide("conversation");
    f.finish();
    expect(await restoring).toBe(false);
    expect(f.hidden()).toBe("conversation");
  });

  it("a second toggle reverses a pending restore", async () => {
    const f = fixture("stage");
    const restoring = f.show("stage");
    f.toggle("stage");
    f.finish();
    expect(await restoring).toBe(false);
    expect(f.hidden()).toBe("stage");
  });
});
