import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");

function readText(rel: string): string {
  return readFileSync(join(denSrc, rel), "utf8");
}

describe("chat scroll invariants: nothing internally scrolls within chat", () => {
  const fileEditDiffCss = readText("file-edit-diff.css");

  it("gives diffs in chat their natural height", () => {
    expect(fileEditDiffCss).toMatch(
      /\.den-file-edit-diff\s+\.den-source-reader,\s*\.den-file-edit-diff\s+\.den-source-reader__editor\s*>\s*\.cm-editor\s*\{[^}]*height:\s*auto;/s,
    );

    expect(fileEditDiffCss).toMatch(
      /\.den-file-edit-diff\s+\.den-source-reader__editors\s*\{[^}]*flex:\s*none;/s,
    );
  });

  it("passes vertical gestures from CodeMirror scroller to the chat stream", () => {
    expect(fileEditDiffCss).toMatch(
      /\.den-file-edit-diff\s+\.cm-scroller\s*\{[^}]*overscroll-behavior-y:\s*auto;/s,
    );
  });
});
