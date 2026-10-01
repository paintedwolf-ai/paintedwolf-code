import { describe, expect, it } from "vitest";
import { formatSentenceCase } from "./format-sentence-case.ts";

describe("formatSentenceCase", () => {
  it("formats machine labels without changing acronyms", () => {
    expect(formatSentenceCase("host_resource_ID")).toBe("Host resource ID");
  });
});
