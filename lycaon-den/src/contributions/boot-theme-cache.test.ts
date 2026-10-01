// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import type { ContributionTheme } from "../api/types.ts";
import {
  BOOT_THEME_STORAGE_KEY,
  clearBootPaletteForTests,
  rememberBootPalette,
} from "./boot-theme-cache.ts";

const indexHtml = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "..", "index.html"),
  "utf8",
);

/** The boot script lives inline in index.html and cannot be imported. */
type BootReplay = {
  /** The root the boot script stamped. */
  root: HTMLElement;
  /** The scheme-scoped rule it injected, or "" when it replayed nothing. */
  css: string;
};

function runBootReplay(options: {
  store: Record<string, string>;
  prefersDark: boolean;
}): BootReplay {
  const source = [...indexHtml.matchAll(/<script>([\s\S]*?)<\/script>/g)]
    .map((match) => match[1]!)
    .find((block) => block.includes(BOOT_THEME_STORAGE_KEY));
  expect(
    source,
    `no inline boot script in index.html reads ${BOOT_THEME_STORAGE_KEY}`,
  ).toBeTruthy();

  const root = document.createElement("div");
  const head = document.createElement("head");
  const fakeWindow = {
    matchMedia: (query: string) => ({
      matches: query.includes("dark") && options.prefersDark,
    }),
  };
  const fakeStorage = {
    getItem: (key: string) => options.store[key] ?? null,
  };
  // The production boot script is executable-only inline source.
  // eslint-disable-next-line @typescript-eslint/no-implied-eval
  new Function("window", "document", "localStorage", source!)(
    fakeWindow,
    {
      documentElement: root,
      head,
      createElement: (tag: string) => document.createElement(tag),
    },
    fakeStorage,
  );
  return { root, css: head.textContent ?? "" };
}

function theme(
  id: string,
  appearance: "light" | "dark",
  background: string,
): ContributionTheme {
  return {
    id,
    name: id,
    appearance,
    tokens: { background, text: "#000000" },
    syntax: {},
    icon_stroke: { weight: 1, cap: "round", join: "round" },
    window_colors: { main: "#a85830", anchors: ["#a85830", "#779933"], hue_spread: 24, chroma_min: 0.04, chroma_max: 0.16 },
    agent_colors: { main: "#6e7689", lightness_step: 0.1, chroma: 0.025 },
    logomark: "shown",
  };
}

function memo(): Record<string, string> {
  return { [BOOT_THEME_STORAGE_KEY]: localStorage.getItem(BOOT_THEME_STORAGE_KEY)! };
}

afterEach(() => clearBootPaletteForTests());

describe("boot theme memo", () => {
  it("replays the paint the last window painted", () => {
    rememberBootPalette("dark", {
      light: null,
      dark: theme("acme/kit:nightshade", "dark", "#0b0b10"),
    });

    const { root, css } = runBootReplay({ store: memo(), prefersDark: true });

    expect(root.getAttribute("data-den-appearance")).toBe("dark");
    expect(root.style.colorScheme).toBe("dark");
    expect(css).toContain("--den-background:#0b0b10");
  });

  it("paints the chosen mode, not the OS scheme", () => {
    rememberBootPalette("light", {
      light: theme("acme/kit:paper", "light", "#fdfcfb"),
      dark: null,
    });

    const { root, css } = runBootReplay({ store: memo(), prefersDark: true });

    expect(root.getAttribute("data-den-appearance")).toBe("light");
    expect(css).toContain("--den-background:#fdfcfb");
  });

  it("scopes the replay to the scheme it belongs to", () => {
    rememberBootPalette("light", {
      light: theme("acme/kit:paper", "light", "#fdfcfb"),
      dark: null,
    });

    const { css } = runBootReplay({ store: memo(), prefersDark: true });

    expect(css).toContain('[data-den-appearance="light"]');
    expect(css).not.toContain('[data-den-appearance="dark"]');
  });

  it("keeps both schemes, so `system` boots right whichever way the OS flipped", () => {
    rememberBootPalette("system", {
      light: theme("acme/kit:paper", "light", "#fdfcfb"),
      dark: theme("acme/kit:nightshade", "dark", "#0b0b10"),
    });

    const store = memo();
    expect(runBootReplay({ store, prefersDark: false }).css).toContain(
      "--den-background:#fdfcfb",
    );
    expect(runBootReplay({ store, prefersDark: true }).css).toContain(
      "--den-background:#0b0b10",
    );
  });

  it("stamps the scheme even with no paint for it yet", () => {
    rememberBootPalette("dark", { light: null, dark: null });

    const { root, css } = runBootReplay({ store: memo(), prefersDark: false });

    expect(root.getAttribute("data-den-appearance")).toBe("dark");
    expect(css).toBe("");
  });

  it("carries the logomark the theme quieted", () => {
    const quiet = theme("acme/kit:paper", "light", "#fdfcfb");
    quiet.logomark = "hidden";
    rememberBootPalette("light", { light: quiet, dark: null });

    const { root } = runBootReplay({ store: memo(), prefersDark: false });

    expect(root.getAttribute("data-den-logomark")).toBe("hidden");
  });

  it("replaces stale scheme paints with stock fallbacks", () => {
    rememberBootPalette("system", {
      light: theme("acme/kit:paper", "light", "#fdfcfb"),
      dark: theme("acme/kit:nightshade", "dark", "#0b0b10"),
    });
    rememberBootPalette("system", { light: null, dark: null });

    const store = memo();
    expect(runBootReplay({ store, prefersDark: false }).css).toBe("");
    expect(runBootReplay({ store, prefersDark: true }).css).toBe("");
  });

  it("falls through to the baked stylesheet on an unreadable memo", () => {
    const { root, css } = runBootReplay({
      store: { [BOOT_THEME_STORAGE_KEY]: "{not json" },
      prefersDark: true,
    });

    expect(root.getAttribute("data-den-appearance")).toBeNull();
    expect(css).toBe("");
  });
});

describe("boot fallback styles", () => {
  it("prefers the memo's values over the stock literals", () => {
    // Pinned to window_backdrop.rs stock literals.
    expect(indexHtml).toContain("var(--den-background, #ffffff)");
    expect(indexHtml).toContain("var(--den-background, #191817)");
    expect(indexHtml).toContain("#191817");
  });

  it("lets a stamped appearance opt out of the OS media query", () => {
    expect(indexHtml).toContain("html:not([data-den-appearance])");
  });
});
