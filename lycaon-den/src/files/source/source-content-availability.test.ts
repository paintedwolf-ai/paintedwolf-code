import { describe, expect, it } from "vitest";
import { sourceContentIsReadable, sourceContentNotice } from "./source-content-availability.ts";

describe("sourceContentNotice", () => {
  it("has nothing to say about readable or absent sides", () => {
    expect(sourceContentNotice({ availability: "available" })).toBeNull();
    expect(sourceContentNotice({ availability: "absent" })).toBeNull();
  });

  it("treats expected boundaries as info", () => {
    for (const availability of ["not_captured", "binary", "directory"] as const) {
      expect(sourceContentNotice({ availability })?.tone).toBe("info");
    }
  });

  it("treats sides the host could not produce as errors", () => {
    for (const availability of ["unresolved", "unavailable"] as const) {
      expect(sourceContentNotice({ availability })?.tone).toBe("error");
    }
  });

  it("names the measured size when the host says a version was too large", () => {
    const sized = sourceContentNotice({ availability: "not_captured", reason: "content_too_large", sizeBytes: 5 * 1024 * 1024 });
    expect(sized?.message).toContain("5.0 MB");
    const unsized = sourceContentNotice({ availability: "not_captured", reason: "content_too_large" });
    expect(unsized?.message).not.toBe(sourceContentNotice({ availability: "not_captured" })?.message);
  });

  it("reads a side as readable only when it has text", () => {
    expect(sourceContentIsReadable("available")).toBe(true);
    expect(sourceContentIsReadable("absent")).toBe(true);
    expect(sourceContentIsReadable("not_captured")).toBe(false);
  });
});
