import { describe, expect, it } from "vitest";
import {
  FONT_FAMILY_MAX_LENGTH,
  SYSTEM_FONT,
  bundledFontsInRole,
  isGuaranteedFont,
  lookupBundledFont,
  resolveFontSelection,
  resolveFontStack,
  sanitizeFontFamily,
} from "./font-catalog.ts";
import { DEN_BUNDLED_FONTS, DEN_FONT_DEFAULTS, DEN_FONT_FALLBACKS } from "./font-catalog.generated.ts";

describe("bundled catalog", () => {
  it("carries a face for every family it offers", () => {
    expect(bundledFontsInRole("ui").length).toBeGreaterThan(0);
    expect(bundledFontsInRole("mono").length).toBeGreaterThan(0);
    for (const font of DEN_BUNDLED_FONTS) {
      expect(font.stack).toContain(`"${font.family}"`);
      expect(font.description).not.toBe("");
    }
  });

  it("defaults to a family it actually bundles", () => {
    expect(lookupBundledFont("ui", DEN_FONT_DEFAULTS.ui)).toBeDefined();
    expect(lookupBundledFont("mono", DEN_FONT_DEFAULTS.mono)).toBeDefined();
  });

  it("keeps roles apart", () => {
    expect(lookupBundledFont("ui", DEN_FONT_DEFAULTS.mono)).toBeUndefined();
    expect(lookupBundledFont("mono", DEN_FONT_DEFAULTS.ui)).toBeUndefined();
  });
});

describe("sanitizeFontFamily", () => {
  it("keeps real family names", () => {
    expect(sanitizeFontFamily("  IBM Plex Sans ")).toBe("IBM Plex Sans");
    expect(sanitizeFontFamily("Fira Code")).toBe("Fira Code");
    expect(sanitizeFontFamily("SF Mono")).toBe("SF Mono");
    expect(sanitizeFontFamily("Noto Sans JP")).toBe("Noto Sans JP");
    expect(sanitizeFontFamily("ヒラギノ角ゴ")).toBe("ヒラギノ角ゴ");
  });

  it("collapses interior whitespace, newlines included", () => {
    expect(sanitizeFontFamily("IBM   Plex\tSans")).toBe("IBM Plex Sans");
    expect(sanitizeFontFamily("Inter\nmono")).toBe("Inter mono");
  });

  it("rejects anything that could close the declaration", () => {
    expect(sanitizeFontFamily('Inter"; background: url(http://x)')).toBeNull();
    expect(sanitizeFontFamily("Inter; color: red")).toBeNull();
    expect(sanitizeFontFamily("Inter}")).toBeNull();
    expect(sanitizeFontFamily("url(evil)")).toBeNull();
    expect(sanitizeFontFamily("Inter\\")).toBeNull();
    expect(sanitizeFontFamily("Inter\n; color: red")).toBeNull();
  });

  it("rejects empty and overlong names", () => {
    expect(sanitizeFontFamily("   ")).toBeNull();
    expect(sanitizeFontFamily("a".repeat(FONT_FAMILY_MAX_LENGTH + 1))).toBeNull();
  });
});

describe("resolveFontStack", () => {
  it("resolves the sentinel to the platform stack", () => {
    expect(resolveFontStack("ui", SYSTEM_FONT)).toBe(DEN_FONT_FALLBACKS.ui);
    expect(resolveFontStack("mono", SYSTEM_FONT)).toBe(DEN_FONT_FALLBACKS.mono);
  });

  it("puts the platform stack behind a bundled family", () => {
    const stack = resolveFontStack("ui", DEN_FONT_DEFAULTS.ui);
    expect(stack.startsWith(`"${DEN_FONT_DEFAULTS.ui}"`)).toBe(true);
    expect(stack.endsWith(DEN_FONT_FALLBACKS.ui)).toBe(true);
  });

  it("quotes a custom family and keeps the fallback behind it", () => {
    expect(resolveFontStack("mono", "Cascadia Code")).toBe(
      `"Cascadia Code", ${DEN_FONT_FALLBACKS.mono}`,
    );
  });

  it("falls back rather than emitting an unusable value", () => {
    expect(resolveFontStack("ui", 'Inter"; color: red')).toBe(DEN_FONT_FALLBACKS.ui);
  });
});

describe("resolveFontSelection", () => {
  it("starts a fresh install on the product default", () => {
    expect(resolveFontSelection("ui", undefined)).toBe(DEN_FONT_DEFAULTS.ui);
    expect(resolveFontSelection("mono", undefined)).toBe(DEN_FONT_DEFAULTS.mono);
  });

  it("keeps the sentinel", () => {
    expect(resolveFontSelection("ui", SYSTEM_FONT)).toBe(SYSTEM_FONT);
  });

  it("keeps a family this machine does not have", () => {
    expect(resolveFontSelection("mono", "Berkeley Mono")).toBe("Berkeley Mono");
  });

  it("drops a value that could not be a family name", () => {
    expect(resolveFontSelection("ui", "}{;")).toBe(DEN_FONT_DEFAULTS.ui);
  });
});

describe("isGuaranteedFont", () => {
  it("is true only for what the app can promise", () => {
    expect(isGuaranteedFont("ui", SYSTEM_FONT)).toBe(true);
    expect(isGuaranteedFont("ui", DEN_FONT_DEFAULTS.ui)).toBe(true);
    expect(isGuaranteedFont("mono", "Fira Code")).toBe(false);
  });
});
