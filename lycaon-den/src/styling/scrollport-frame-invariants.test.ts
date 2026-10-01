import { readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { walkFiles } from "../test/style-contracts/inventory.ts";
import { splitStylesheetRules } from "../test/style-contracts/stylesheet-parser.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");

/** Returns top-level declarations without nested blocks. */
function topLevelDeclarations(body: string): string {
  let depth = 0;
  let declarations = "";
  for (const char of body) {
    if (char === "{") depth++;
    else if (char === "}") depth--;
    else if (depth === 0) declarations += char;
  }
  return declarations;
}

describe("scrollport frame invariants", () => {
  it("ensures scrollport frames carry no padding, keeping padding on content extents", () => {
    // The frame sizes the scrollbar track; padding belongs to the content extent.
    const FRAME_PATTERNS = [
      /den-chat-stream-frame/,
      /\[data-den-scrollport\]/,
      /\.den-scrollport(?!\w)/,
    ];

    const violations: string[] = [];

    const scan = (rel: string, css: string) => {
      for (const { selector, body } of splitStylesheetRules(css)) {
        if (body.includes("{")) scan(rel, body);
        const matchesFrame = FRAME_PATTERNS.some((p) => p.test(selector));
        if (!matchesFrame) continue;

        const isFrameItself = selector.split(",").some((part) => {
          const trimmed = part.trim();
          return (
            FRAME_PATTERNS.some((p) => p.test(trimmed)) &&
            !trimmed.includes(">") &&
            !trimmed.includes(" ")
          );
        });
        if (!isFrameItself) continue;

        const decls = topLevelDeclarations(body);
        const paddingMatch = decls.match(/padding(?:-(?:top|bottom|left|right))?\s*:\s*([^;]+)/);
        if (paddingMatch) {
          const val = paddingMatch[1]?.trim() ?? "";
          if (val !== "0" && val !== "0px" && val !== "none") {
            violations.push(`${rel}: ${selector} declares frame padding: ${val}`);
          }
        }
      }
    };

    for (const path of walkFiles(denSrc, (p) => p.endsWith(".css"))) {
      scan(relative(denSrc, path), readFileSync(path, "utf8"));
    }

    expect(violations).toEqual([]);
  });

  it("ensures all custom surfaces calling attachThemedViewportScrollbar supply an extent element", () => {
    // Attached viewports declare their extent through a content element, logical model, or scheduler.
    const SCANNED_EXTENSIONS = [".ts", ".tsx"];
    const EXCLUDED = [
      "platform/scrolling/themed-scrollbars.ts",
      "platform/scrolling/themed-scrollbars.test.ts",
      "platform/scrolling/themed-scrollbars.geometry.test.ts",
      "platform/scrolling/themed-scrollbars.measurement.test.ts",
      "platform/scrolling/themed-scrollbars.discovery.test.ts",
      "platform/scrolling/themed-scrollbars.selection.test.ts",
    ];

    const violations: string[] = [];

    for (const path of walkFiles(denSrc, (p) => SCANNED_EXTENSIONS.some((ext) => p.endsWith(ext)))) {
      const rel = relative(denSrc, path);
      if (EXCLUDED.includes(rel) || rel.includes(".test.") || rel.includes("/test/")) continue;
      const src = readFileSync(path, "utf8");

      for (const match of src.matchAll(/\battachThemedViewportScrollbar\s*\(([^)]+)\)/g)) {
        const callContent = match[1] ?? "";
        const hasExtentTracking =
          callContent.includes("extent") ||
          callContent.includes("vertical") ||
          callContent.includes("schedule");
        if (!hasExtentTracking) {
          const line = src.slice(0, match.index).split("\n").length;
          violations.push(
            `${rel}:${line}: attachThemedViewportScrollbar called without extent, vertical, or schedule`,
          );
        }
      }
    }

    expect(violations).toEqual([]);
  });
});
