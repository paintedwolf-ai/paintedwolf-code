import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { readSourceText } from "../test/stylesheet-source.ts";
import { extractCssRuleBlock } from "./css-contract.ts";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const utilities = readSourceText(join(root, "shell-utilities.css"), "utf8");
const navigation = readSourceText(join(root, "styling/chrome/project-navigation.css"), "utf8");

describe("project rail and dock wash", () => {
  it("keeps rows clear of the wash at the scroll end and when focus scrolls them into view", () => {
    // The wash paints over the rail's last rows above the dock.
    expect(extractCssRuleBlock(navigation, ".den-shell-aside-main > .den-shell-nav-dock::after"))
      .toMatch(/height:\s*var\(--den-nav-wash-height\)/);
    // Focus reveal (short windows, keyboard traversal) stops above the wash.
    expect(extractCssRuleBlock(utilities, ".den-shell-nav"))
      .toMatch(/scroll-padding-block-end:\s*var\(--den-nav-wash-height\)/);
    // The scroll end leaves a tail as tall as the wash.
    expect(extractCssRuleBlock(utilities, ".den-shell-nav-content"))
      .toMatch(/flex:\s*0 0 var\(--den-nav-wash-height\)/);
  });
});
