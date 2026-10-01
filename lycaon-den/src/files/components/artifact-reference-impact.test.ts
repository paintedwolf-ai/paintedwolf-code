import { describe, expect, it } from "vitest";
import type { ArtifactListItem } from "../../api/types.ts";
import {
  deleteConfirmBody,
  referenceImpactSentence,
} from "./artifact-reference-impact.ts";

function item(references?: ArtifactListItem["references"]): ArtifactListItem {
  return {
    id: "art-1",
    mime: "image/png",
    source: "render",
    session_id: "sess-1",
    created_at: "2026-07-01T12:00:00Z",
    references,
  } as ArtifactListItem;
}

describe("artifact reference impact", () => {
  it("says nothing refers to an unclaimed artifact", () => {
    expect(referenceImpactSentence(item())).toBe("");
    expect(referenceImpactSentence(item([]))).toBe("");
  });

  it("names one claim in singular", () => {
    expect(
      referenceImpactSentence(item([{ kind: "message_present", count: 1 }])),
    ).toBe("Used by 1 message that shows it.");
  });

  it("orders claims by consequence and joins them", () => {
    const sentence = referenceImpactSentence(
      item([
        { kind: "tool_result", count: 3 },
        { kind: "message_present", count: 2 },
        { kind: "project_cover", count: 1 },
      ]),
    );
    expect(sentence).toBe(
      "Used by the project cover, 2 messages that show it and 3 tool results.",
    );
  });

  it("sums repeated kinds and ignores empty or unknown ones", () => {
    const target = item([
      { kind: "message_present", count: 1 },
      { kind: "message_present", count: 2 },
      { kind: "tool_result", count: 0 },
      { kind: "made_up" as never, count: 9 },
    ]);
    expect(referenceImpactSentence(target)).toBe(
      "Used by 3 messages that show it.",
    );
  });

  it("always warns that the reference survives the delete", () => {
    // Deleting removes bytes, never the id.
    for (const target of [item(), item([{ kind: "project_cover", count: 1 }])]) {
      expect(deleteConfirmBody(target)).toContain("will read as deleted");
    }
    expect(deleteConfirmBody(item([{ kind: "project_cover", count: 1 }]))).toContain(
      "the project cover",
    );
  });
});
