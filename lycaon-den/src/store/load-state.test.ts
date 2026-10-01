import { describe, expect, it } from "vitest";

import {
  errorOf,
  isLoaded,
  loadFailed,
  loaded,
  loading,
  settledEmpty,
  unloaded,
  valueOf,
} from "./load-state";

describe("load-state", () => {
  it("settledEmpty is the only state that reads as a genuine empty answer", () => {
    const empty = (v: string[]) => v.length === 0;
    expect(settledEmpty(loaded<string[]>([]), empty)).toBe(true);
    expect(settledEmpty(loaded(["pin"]), empty)).toBe(false);
    // Unsettled states are not empty answers.
    expect(settledEmpty(unloaded<string[]>(), empty)).toBe(false);
    expect(settledEmpty(loading<string[]>(), empty)).toBe(false);
    expect(settledEmpty(loadFailed<string[]>(new Error("boom")), empty)).toBe(false);
  });

  it("a failed refresh keeps the previously shown value", () => {
    const shown = loaded(["a", "b"]);
    const failing = loadFailed(new Error("sidecar restarting"), loading(shown));
    expect(valueOf(failing)).toEqual(["a", "b"]);
    expect(errorOf(failing)).toBe("sidecar restarting");
    // Stale-but-shown is still not a settled answer.
    expect(settledEmpty(failing, (v) => v.length === 0)).toBe(false);
    expect(isLoaded(failing)).toBe(false);
  });

  it("a first load has nothing to carry", () => {
    const failing = loadFailed<string[]>(new Error("boom"), loading(unloaded()));
    expect(valueOf(failing)).toBeUndefined();
    expect(errorOf(failing)).toBe("boom");
  });

  it("non-Error rejections still surface as text", () => {
    expect(errorOf(loadFailed<number>("string reason"))).toBe("string reason");
    // A blank Error message falls back to the error's string form.
    expect(errorOf(loadFailed<number>(new Error(" ")))).toBe(String(new Error(" ")));
  });

  it("a successful reload replaces the carried value", () => {
    const reloaded = loaded(["fresh"]);
    expect(isLoaded(reloaded) && reloaded.value).toEqual(["fresh"]);
    expect(errorOf(reloaded)).toBeUndefined();
  });
});
