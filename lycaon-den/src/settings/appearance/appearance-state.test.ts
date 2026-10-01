import { describe, expect, it } from "vitest";
import { mergeAppearancePrefs } from "./appearance-state.ts";

describe("mergeAppearancePrefs", () => {
  it("preserves theme and font choices across either editor", () => {
    const current = {
      mode: "system" as const,
      lightTheme: "acme/day",
      darkTheme: "acme/night",
      uiFont: "Literata",
      monoFont: "Berkeley Mono",
    };

    expect(mergeAppearancePrefs(current, { mode: "dark" })).toEqual({
      ...current,
      mode: "dark",
    });
    expect(mergeAppearancePrefs(current, { monoFont: "system" })).toEqual({
      ...current,
      monoFont: "system",
    });
  });
});
