// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import {
  BOOT_FONT_STORAGE_KEY,
  clearBootFontsForTests,
  rememberBootFonts,
} from "./boot-font-cache.ts";
import { FONT_ROLE_VARS } from "./font-application.ts";
import { SYSTEM_FONT } from "./font-catalog.ts";
import { DEN_FONT_DEFAULTS, DEN_FONT_FALLBACKS } from "./font-catalog.generated.ts";

const indexHtml = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "..", "index.html"),
  "utf8",
);

/** Runs the inline boot replay script extracted from index.html. */
function runBootReplay(store: Record<string, string>): HTMLElement {
  const source = [...indexHtml.matchAll(/<script>([\s\S]*?)<\/script>/g)]
    .map((match) => match[1]!)
    .find((block) => block.includes(BOOT_FONT_STORAGE_KEY));
  expect(
    source,
    `no inline boot script in index.html reads ${BOOT_FONT_STORAGE_KEY}`,
  ).toBeTruthy();

  const root = document.createElement("div");
  // Evaluates the inline boot script in a mock environment.
  // eslint-disable-next-line @typescript-eslint/no-implied-eval
  new Function("window", "document", "localStorage", source!)(
    { matchMedia: () => ({ matches: false }) },
    {
      documentElement: root,
      head: document.createElement("head"),
      createElement: (tag: string) => document.createElement(tag),
    },
    { getItem: (key: string) => store[key] ?? null },
  );
  return root;
}

function memo(): Record<string, string> {
  return {
    [BOOT_FONT_STORAGE_KEY]: localStorage.getItem(BOOT_FONT_STORAGE_KEY)!,
  };
}

afterEach(() => clearBootFontsForTests());

describe("boot font memo", () => {
  it("replays the type the last window painted", () => {
    rememberBootFonts({ ui: "Literata", mono: SYSTEM_FONT });

    const root = runBootReplay(memo());

    expect(root.style.getPropertyValue(FONT_ROLE_VARS.ui)).toContain('"Literata"');
    expect(root.style.getPropertyValue(FONT_ROLE_VARS.mono)).toBe(
      DEN_FONT_FALLBACKS.mono,
    );
  });

  it("memos the resolved stack, not the stored selection", () => {
    rememberBootFonts({ ui: DEN_FONT_DEFAULTS.ui, mono: DEN_FONT_DEFAULTS.mono });

    const stored = JSON.parse(localStorage.getItem(BOOT_FONT_STORAGE_KEY)!) as {
      ui: string;
      mono: string;
    };

    expect(stored.ui).toContain(DEN_FONT_FALLBACKS.ui);
    expect(stored.mono).toContain(DEN_FONT_FALLBACKS.mono);
  });

  it("paints the generated defaults when nothing was memoed", () => {
    const root = runBootReplay({});

    expect(root.style.getPropertyValue(FONT_ROLE_VARS.ui)).toBe("");
    expect(root.style.getPropertyValue(FONT_ROLE_VARS.mono)).toBe("");
  });

  it("ignores a memo from a shape it does not know", () => {
    const root = runBootReplay({
      [BOOT_FONT_STORAGE_KEY]: JSON.stringify({ ui: 1, mono: 2 }),
    });

    expect(root.style.getPropertyValue(FONT_ROLE_VARS.ui)).toBe("");
  });

  it("survives a corrupt memo", () => {
    const root = runBootReplay({ [BOOT_FONT_STORAGE_KEY]: "{not json" });

    expect(root.style.getPropertyValue(FONT_ROLE_VARS.ui)).toBe("");
  });
});
