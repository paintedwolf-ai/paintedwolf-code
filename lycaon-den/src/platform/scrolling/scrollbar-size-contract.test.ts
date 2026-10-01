import { readFileSync } from "node:fs";
import { readSourceText } from "../../test/stylesheet-source.ts";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const srcDir = join(dirname(fileURLToPath(import.meta.url)), "../..");

describe("scrollbar size contract", () => {
  it("shares the accessible width across overlay and chat chrome", () => {
    const global = readFileSync(join(srcDir, "global.css"), "utf8");
    const theme = readFileSync(
      join(srcDir, "platform/themed-scrollbars.css"),
      "utf8",
    );
    const chat = readSourceText(join(srcDir, "chat-utilities.css"), "utf8");

    expect(global).toMatch(/--den-scrollbar-size:\s*12px/);
    expect(theme).toMatch(/--os-size:\s*var\(--den-scrollbar-size\)/);
    expect(chat).toMatch(
      /--chat-scrollbar-size:\s*var\(--den-scrollbar-size\)/,
    );
  });

  it("keeps the rail handle flush with its edge at the shared grab width", () => {
    const theme = readFileSync(
      join(srcDir, "platform/themed-scrollbars.css"),
      "utf8",
    );
    const rail = /\.den-shell-nav-frame > \.os-theme-den\.os-scrollbar\s*\{([^}]*)\}/.exec(theme)?.[1] ?? "";
    const size = Number(/--os-size:\s*(\d+)px/.exec(rail)?.[1]);
    const padding = Number(/--os-padding-perpendicular:\s*(\d+)px/.exec(rail)?.[1]);
    const reach = Number(/--os-handle-interactive-area-offset:\s*(\d+)px/.exec(theme)?.[1]);
    const global = readFileSync(join(srcDir, "global.css"), "utf8");
    const shared = Number(/--den-scrollbar-size:\s*(\d+)px/.exec(global)?.[1]);

    // An 8px handle against the edge stays clear of the rail's 10px row gutter.
    expect(size).toBe(8);
    expect(padding).toBe(0);
    // The handle's interactive area reaches inward to the width every other scrollbar offers.
    expect(size + reach).toBe(shared);
  });

  it("keeps tree scrollbars above pinned folder rows", () => {
    const theme = readFileSync(
      join(srcDir, "platform/themed-scrollbars.css"),
      "utf8",
    );

    expect(theme).toMatch(
      /\.den-files-tree-scroll-frame\s*>\s*\.os-scrollbar\s*\{[^}]*z-index:\s*4/,
    );
  });
});
