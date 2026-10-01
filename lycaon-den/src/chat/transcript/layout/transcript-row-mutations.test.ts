// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { observeTranscriptRowMutations } from "./transcript-row-mutations.ts";

afterEach(() => { vi.restoreAllMocks(); document.body.replaceChildren(); });
describe("transcript row mutations", () => {
  it("delivers unique dirty rows without inserting an animation frame", async () => {
    const frame = vi.spyOn(globalThis, "requestAnimationFrame");
    const root = document.createElement("section");
    root.innerHTML = '<div class="transcript-viewport-row"><span></span></div><div class="transcript-viewport-row"></div>';
    document.body.append(root);
    const rows = [...root.children] as HTMLElement[];
    const changed = vi.fn();
    const stop = observeTranscriptRowMutations(root, changed);
    rows[0]!.firstChild!.textContent = "first";
    rows[0]!.append(document.createElement("strong"));
    rows[1]!.textContent = "second";
    await Promise.resolve();
    expect(changed).toHaveBeenCalledExactlyOnceWith(rows);
    expect(frame).not.toHaveBeenCalled();
    stop();
  });
  it("ignores runway and detached rows and cancels undelivered mutations", async () => {
    const root = document.createElement("section");
    root.innerHTML = '<div class="den-transcript-virtual-runway"></div><div class="transcript-viewport-row"></div>';
    document.body.append(root);
    const changed = vi.fn();
    const stop = observeTranscriptRowMutations(root, changed);
    root.firstChild!.textContent = "runway";
    const row = root.lastElementChild!;
    row.textContent = "detached";
    row.remove();
    await Promise.resolve();
    expect(changed).not.toHaveBeenCalled();
    root.append(row);
    row.textContent = "pending";
    stop();
    await Promise.resolve();
    expect(changed).not.toHaveBeenCalled();
  });
});
