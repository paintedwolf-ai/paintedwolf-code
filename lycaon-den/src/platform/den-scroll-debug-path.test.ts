import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { defaultDenScrollDebugFile } from "../../vite.den-scroll-debug.ts";

describe("Den scroll debug path", () => {
  it("keeps runtime logs under the configured application state", () => {
    const configDir = resolve("/tmp", "paintedwolf-test-config");

    expect(defaultDenScrollDebugFile(configDir)).toBe(
      join(configDir, "debug", "den-scroll.jsonl"),
    );
  });
});
