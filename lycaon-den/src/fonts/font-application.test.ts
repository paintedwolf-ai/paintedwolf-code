// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  FONT_ROLE_VARS,
  applyFontRole,
  applyFontSelections,
  warmFontSelections,
} from "./font-application.ts";
import { SYSTEM_FONT } from "./font-catalog.ts";
import { DEN_FONT_DEFAULTS, DEN_FONT_FALLBACKS } from "./font-catalog.generated.ts";

afterEach(() => {
  document.documentElement.style.removeProperty(FONT_ROLE_VARS.ui);
  document.documentElement.style.removeProperty(FONT_ROLE_VARS.mono);
  Reflect.deleteProperty(document, "fonts");
  vi.restoreAllMocks();
});

function readVar(name: string): string {
  return document.documentElement.style.getPropertyValue(name);
}

/** jsdom carries no font set, so the warm path needs one to observe. */
function stubFontFaces() {
  const load = vi.fn(async () => []);
  Object.defineProperty(document, "fonts", {
    configurable: true,
    value: { load },
  });
  return load;
}

describe("font application", () => {
  it("writes one custom property per role", () => {
    applyFontSelections({ ui: DEN_FONT_DEFAULTS.ui, mono: SYSTEM_FONT });

    expect(readVar(FONT_ROLE_VARS.ui)).toContain(`"${DEN_FONT_DEFAULTS.ui}"`);
    expect(readVar(FONT_ROLE_VARS.mono)).toBe(DEN_FONT_FALLBACKS.mono);
  });

  it("replaces rather than accumulating", () => {
    applyFontRole("ui", DEN_FONT_DEFAULTS.ui);
    applyFontRole("ui", SYSTEM_FONT);

    expect(readVar(FONT_ROLE_VARS.ui)).toBe(DEN_FONT_FALLBACKS.ui);
  });

  it("keeps a stored value that could not be a family out of the stylesheet", () => {
    applyFontRole("ui", 'Inter"; background: url(http://x)');

    expect(readVar(FONT_ROLE_VARS.ui)).toBe(DEN_FONT_FALLBACKS.ui);
  });

  it("fetches the bundled faces so no first use waits mid-transition", () => {
    const load = stubFontFaces();

    warmFontSelections({
      ui: DEN_FONT_DEFAULTS.ui,
      mono: DEN_FONT_DEFAULTS.mono,
    });

    expect(load).toHaveBeenCalledWith(`1rem "${DEN_FONT_DEFAULTS.ui}"`);
    expect(load).toHaveBeenCalledWith(`1rem "${DEN_FONT_DEFAULTS.mono}"`);
  });

  it("asks for nothing the bundle does not carry", () => {
    const load = stubFontFaces();

    warmFontSelections({ ui: SYSTEM_FONT, mono: "Some Installed Face" });

    expect(load).not.toHaveBeenCalled();
  });

  it("does nothing where the document has no font set", () => {
    expect(() =>
      warmFontSelections({
        ui: DEN_FONT_DEFAULTS.ui,
        mono: DEN_FONT_DEFAULTS.mono,
      }),
    ).not.toThrow();
  });
});
