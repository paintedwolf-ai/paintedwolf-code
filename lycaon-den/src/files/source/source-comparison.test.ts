import { describe, expect, it } from "vitest";
import type { SourceComparison } from "../../api/types.ts";
import { comparisonEndpoints } from "./source-comparison.ts";

function side(content: string): NonNullable<SourceComparison["before"]> {
  return {
    state: "content",
    size_bytes: content.length,
    availability: "available",
    content,
  };
}

describe("comparisonEndpoints", () => {
  it("returns both endpoints when the lens holds the file", () => {
    const endpoints = comparisonEndpoints({
      in_range: true,
      location_changed: false,
      before: side("was\n"),
      after: side("is\n"),
    });
    expect(endpoints?.before.content).toBe("was\n");
    expect(endpoints?.after.content).toBe("is\n");
  });

  it("returns null out of range, so nothing paints an empty document as history", () => {
    expect(
      comparisonEndpoints({ in_range: false, location_changed: false }),
    ).toBeNull();
  });

  it("returns null when either endpoint is missing", () => {
    expect(
      comparisonEndpoints({
        in_range: true,
        location_changed: false,
        before: side("was\n"),
      }),
    ).toBeNull();
  });

  it("returns null for a comparison that never loaded", () => {
    expect(comparisonEndpoints(null)).toBeNull();
    expect(comparisonEndpoints(undefined)).toBeNull();
  });
});
