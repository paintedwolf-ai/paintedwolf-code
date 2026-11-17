// @vitest-environment jsdom
/** Main-workspace placement and peer-window boundaries. */
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { readSourceText } from "./test/stylesheet-source.ts";

const DEN_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

function read(rel: string): string {
  return readSourceText(join(DEN_ROOT, rel));
}

describe("workspace placement contract", () => {
  it("claims stage chrome on every arrival, not only a Context click", () => {
    const shell = read("src/components/shell/Shell.tsx");
    const start = shell.indexOf("const openStageWithPlacement");
    const open = shell.slice(start, shell.indexOf("\n  };", start));
    expect(open).toContain("claimStageChrome");
  });
});
