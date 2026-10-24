import { describe, expect, it } from "vitest";
import { formatBytes } from "./format-bytes.ts";

describe("formatBytes", () => {
  it("formats each magnitude and blanks unknown sizes", () => {
    expect(formatBytes(undefined)).toBe("");
    expect(formatBytes(0)).toBe("");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(formatBytes(3.5 * 1024 ** 3)).toBe("3.5 GB");
    expect(formatBytes(2 * 1024 ** 4)).toBe("2.0 TB");
  });
});
