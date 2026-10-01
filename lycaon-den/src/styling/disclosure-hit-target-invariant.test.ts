import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { readSourceText } from "../test/stylesheet-source.ts";

const srcDir = join(dirname(fileURLToPath(import.meta.url)), "..");

function read(path: string): string {
  return readSourceText(join(srcDir, path));
}

describe("transcript disclosure hit targets", () => {
  it("makes the full card header toggle every shared disclosure", () => {
    expect(read("chat-utilities.css")).toMatch(
      /@utility den-transcript-disclosure-card\s*\{[\s\S]*?& > summary\s*\{[\s\S]*?width:\s*100%/,
    );
  });

  it("gives grounded cards the full transcript width even under short prose", () => {
    expect(read("citation-utilities.css")).toMatch(
      /\.assistant-turn:has\(> \.den-citation-evidence-chicklet\)\s*\{[\s\S]*?width:\s*100%/,
    );
  });
});
