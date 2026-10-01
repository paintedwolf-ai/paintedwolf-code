// @vitest-environment jsdom
import { readSourceText } from "../test/stylesheet-source.ts";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ContributionTheme } from "../api/types.ts";
import {
  applyAppearance,
  applyTheme,
  clearTheme,
  DEFAULT_APPEARANCE,
  resolveActiveTheme,
  STOCK_DARK_THEME,
  STOCK_LIGHT_THEME,
  themePaintKey,
} from "./theme-application.ts";
import { THEME_TOKEN_VARS } from "./theme-vocabulary.generated.ts";

function theme(
  id: string,
  appearance: "light" | "dark",
  tokens: Record<string, string> = {},
  logomark: "shown" | "hidden" = "shown",
): ContributionTheme {
  return {
    id,
    name: id,
    appearance,
    tokens: { background: "#ffffff", text: "#000000", ...tokens },
    syntax: { keyword: { color: "#cf222e", italic: true, bold: true } },
    icon_stroke: { weight: 1, cap: "round", join: "round" },
    window_colors: { main: "#a85830", anchors: ["#a85830", "#779933"], hue_spread: 24, chroma_min: 0.04, chroma_max: 0.16 },
    agent_colors: { main: "#6e7689", lightness_step: 0.1, chroma: 0.025 },
    logomark,
  };
}

const STOCK = [
  theme(STOCK_LIGHT_THEME, "light"),
  theme(STOCK_DARK_THEME, "dark", { background: "#191817", text: "#e4e2df" }),
];

describe("resolveActiveTheme", () => {
  it("picks the theme selected for the resolved scheme", () => {
    const themes = [...STOCK, theme("acme/kit:nightshade", "dark")];
    const resolution = resolveActiveTheme(
      { mode: "dark", lightTheme: STOCK_LIGHT_THEME, darkTheme: "acme/kit:nightshade" },
      themes,
      "dark",
    );
    expect(resolution.theme?.id).toBe("acme/kit:nightshade");
    expect(resolution.selectedId).toBe("acme/kit:nightshade");
  });

  it("falls back to the stock theme of the same appearance, visibly", () => {
    const resolution = resolveActiveTheme(
      { mode: "dark", lightTheme: STOCK_LIGHT_THEME, darkTheme: "acme/gone:nightshade" },
      STOCK,
      "dark",
    );
    expect(resolution.theme?.id).toBe(STOCK_DARK_THEME);
    expect(resolution.selectedId).toBe("acme/gone:nightshade");
  });

  it("refuses a theme whose appearance does not match the scheme", () => {
    const themes = [...STOCK, theme("acme/kit:bright", "light")];
    const resolution = resolveActiveTheme(
      { mode: "dark", lightTheme: STOCK_LIGHT_THEME, darkTheme: "acme/kit:bright" },
      themes,
      "dark",
    );
    expect(resolution.theme?.id).toBe(STOCK_DARK_THEME);
  });

  it("starts from the stock pairing", () => {
    const resolution = resolveActiveTheme(DEFAULT_APPEARANCE, STOCK, "light");
    expect(resolution.theme).not.toBeNull();
  });
});

describe("applyTheme", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
  });
  afterEach(() => root.remove());

  it("writes every token the theme carries as its custom property", () => {
    applyTheme(theme("t", "light", { accent: "#b85c38" }), root);
    expect(root.style.getPropertyValue(THEME_TOKEN_VARS.background!)).toBe("#ffffff");
    expect(root.style.getPropertyValue(THEME_TOKEN_VARS.accent!)).toBe("#b85c38");
  });

  it("stamps the appearance so the scheme follows the theme, not the OS", () => {
    applyTheme(theme("t", "dark"), root);
    expect(root.getAttribute("data-den-appearance")).toBe("dark");
    expect(root.style.colorScheme).toBe("dark");
  });

  it("writes syntax style flags as the CSS the highlight style reads", () => {
    applyTheme(theme("t", "light"), root);
    expect(root.style.getPropertyValue("--den-code-keyword")).toBe("#cf222e");
    expect(root.style.getPropertyValue("--den-code-keyword-style")).toBe("italic");
    expect(root.style.getPropertyValue("--den-code-keyword-weight")).toBe("600");
  });

  it("clears a previous theme's flags rather than leaving them set", () => {
    // Every theme writes explicit style defaults.
    applyTheme(theme("t", "light"), root);
    const plain = theme("u", "light");
    plain.syntax = { keyword: { color: "#0000ff" } };
    applyTheme(plain, root);
    expect(root.style.getPropertyValue("--den-code-keyword-style")).toBe("normal");
    expect(root.style.getPropertyValue("--den-code-keyword-weight")).toBe("inherit");
  });

  it("carries the logomark visibility, and switches it back", () => {
    applyTheme(theme("t", "light", {}, "hidden"), root);
    expect(root.getAttribute("data-den-logomark")).toBe("hidden");
    applyTheme(theme("u", "light"), root);
    expect(root.getAttribute("data-den-logomark")).toBe("shown");
  });

  it("writes brand-field like any other token", () => {
    applyTheme(theme("t", "light", { "brand-field": "#e6e9ef" }), root);
    expect(root.style.getPropertyValue(THEME_TOKEN_VARS["brand-field"]!)).toBe("#e6e9ef");
  });

  it("restores generated stock state wholesale", () => {
    applyTheme(theme("t", "dark", { accent: "#b85c38" }, "hidden"), root);
    clearTheme(root);
    applyAppearance("light", root);

    expect(root.style.getPropertyValue(THEME_TOKEN_VARS.accent!)).toBe("");
    expect(root.getAttribute("data-den-logomark")).toBeNull();
    expect(root.getAttribute("data-den-appearance")).toBe("light");
    expect(root.style.colorScheme).toBe("light");
  });

  it("keys paint by visual content rather than contribution identity", () => {
    const first = theme("acme/one", "light", { accent: "#b85c38" });
    const samePaint = theme("acme/two", "light", { accent: "#b85c38" });
    const changed = theme("acme/one", "light", { accent: "#0055aa" });

    expect(themePaintKey(first)).toBe(themePaintKey(samePaint));
    expect(themePaintKey(first)).not.toBe(themePaintKey(changed));
  });
});

describe("logomark visibility", () => {
  const sheet = readSourceText(
    join(dirname(fileURLToPath(import.meta.url)), "..", "global-components.css"),
    "utf8",
  );

  it("hides the mark without collapsing its box", () => {
    // Visibility preserves the fixed layout box.
    const rule = /:root\[data-den-logomark="hidden"\][^{]*\{([^}]*)\}/.exec(sheet);
    expect(rule, "no rule hides the logomark").not.toBeNull();
    expect(rule![1]).toContain("visibility: hidden");
    expect(rule![1]).not.toContain("display:");
  });

  it("keeps the mark's colors on theme tokens", () => {
    const lockup = readSourceText(
      join(
        dirname(fileURLToPath(import.meta.url)),
        "..",
        "components",
        "primitives",
        "BrandLockup.tsx",
      ),
      "utf8",
    );
    expect(lockup).not.toMatch(/fill="#[0-9a-fA-F]{3,8}"/);
    expect(lockup).toContain("var(--den-brand-field)");
    expect(lockup).toContain("var(--den-brand-glyph-mark)");
  });

  it("does not restate the plate on the lockup", () => {
    // The root token remains theme-controlled.
    expect(sheet).not.toMatch(
      /\.den-brand-lockup\s*\{[^}]*--den-brand-field:/s,
    );
    expect(THEME_TOKEN_VARS["brand-field"]).toBe("--den-brand-field");
  });

  it("paints the bars from the signal the floor gates", () => {
    // Logo bars use the contrast-gated signal token.
    expect(sheet).toMatch(
      /--den-brand-glyph-mark:\s*var\(--den-accent-signal\)/,
    );
    expect(THEME_TOKEN_VARS["accent-signal"]).toBe("--den-accent-signal");
  });
});
