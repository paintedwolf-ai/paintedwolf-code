import { describe, expect, it } from "vitest";
import { IME_COMPOSITION_KEY_CODE, isImeComposition } from "./ime-composition.ts";

describe("input method composition", () => {
  it("counts both the composing flag and the legacy commit key code", () => {
    expect(isImeComposition({ isComposing: true, keyCode: 13 })).toBe(true);
    expect(isImeComposition({ isComposing: false, keyCode: IME_COMPOSITION_KEY_CODE })).toBe(true);
    expect(isImeComposition({ keyCode: IME_COMPOSITION_KEY_CODE })).toBe(true);
    expect(isImeComposition({ isComposing: false, keyCode: 13 })).toBe(false);
    expect(isImeComposition({})).toBe(false);
  });
});
