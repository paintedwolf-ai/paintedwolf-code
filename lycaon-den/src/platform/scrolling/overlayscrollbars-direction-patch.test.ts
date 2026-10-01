import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denRoot = join(import.meta.dirname, "..", "..", "..");
const patchPath = "patches/overlayscrollbars@2.16.0.patch";

describe("OverlayScrollbars direction patch", () => {
  it("uses only the direction published before initialization", () => {
    const packageJson = JSON.parse(
      readFileSync(join(denRoot, "package.json"), "utf8"),
    ) as { patchedDependencies?: Record<string, string> };
    const patch = readFileSync(join(denRoot, patchPath), "utf8");
    const additions = patch
      .split("\n")
      .filter((line) => line.startsWith("+") && !line.startsWith("+++"))
      .join("\n");

    expect(
      packageJson.patchedDependencies?.["overlayscrollbars@2.16.0"],
    ).toBe(patchPath);
    expect(
      additions.match(
        /getDirectionIsRTL = t => getAttr\(t, staticDirectionAttr\) === "rtl"/g,
      ),
    ).toHaveLength(4);
    expect(additions.match(/"rows", staticDirectionAttr/g)).toHaveLength(4);
  });

  it("leaves scroll-driven handle offsets to the host's measure phase", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");
    const lines = patch.split("\n");
    const removals = lines.filter((line) => line.startsWith("-") && !line.startsWith("---"));
    const additions = lines.filter((line) => line.startsWith("+") && !line.startsWith("+++"));

    // The library's scroll listener leaves the offset read to the host.
    expect(removals.filter((line) => /^-\s+A\(\);$/.test(line))).toHaveLength(4);
    expect(additions.some((line) => /\bA\(\);/.test(line))).toBe(false);
  });
});
