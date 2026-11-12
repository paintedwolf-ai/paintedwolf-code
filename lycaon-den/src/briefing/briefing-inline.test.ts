import { describe, expect, it } from "vitest";
import { briefingRevealText, splitBriefingInline } from "./briefing-inline.ts";

describe("splitBriefingInline", () => {
  it("splits single-backtick code spans and leaves fences literal", () => {
    expect(splitBriefingInline("Calls `buildReview` safely.")).toEqual([
      { kind: "text", text: "Calls " },
      { kind: "code", text: "buildReview" },
      { kind: "text", text: " safely." },
    ]);
    expect(splitBriefingInline("Keeps ```raw``` literal.")).toEqual([{ kind: "text", text: "Keeps ```raw``` literal." }]);
  });

  it("reveals the characters without backticks", () => {
    expect(briefingRevealText("Reads `FIXTURE_LISTEN_ADDR` first.")).toBe("Reads FIXTURE_LISTEN_ADDR first.");
  });
});
