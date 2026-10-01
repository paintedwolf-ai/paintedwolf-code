import { describe, expect, it } from "vitest";
import { isAllowlistedConsoleNoise } from "./console-trap.ts";

describe("console trap allowlist", () => {
  it("allowlists reactivity-store traverse perf warnings", () => {
    expect(
      isAllowlistedConsoleNoise([
        "[reactivity-store] 'traverse' current data: %o take a lot of time",
        { nested: true },
      ]),
    ).toBe(true);
  });

  it("does not allowlist arbitrary console noise", () => {
    expect(isAllowlistedConsoleNoise(["boom"])).toBe(false);
    expect(isAllowlistedConsoleNoise([new Error("reactive subscriber threw")])).toBe(false);
  });
});
