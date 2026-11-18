import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denRoot = join(import.meta.dirname, "..", "..");
const patchPath = "patches/@codemirror+view@6.43.6-scroll-and-wrap-heights.patch";

describe("CodeMirror view patch", () => {
  it("applies scroll-anchor corrections relative to the scroller's current offset", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // A relative scroll keeps travel an asynchronous scrolling thread made after the measure read the offset.
    expect(patch.match(/\+\s*scroll\.scrollBy\(0, diff\);/g)).toHaveLength(2);
    expect(patch.match(/-\s*scroll\.scrollTop \+= diff;/g)).toHaveLength(2);
  });
});
