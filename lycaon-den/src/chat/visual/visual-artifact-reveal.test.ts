import { afterEach, describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import {
  clearVisualArtifactSessionMemory,
  noteVisualArtifactIntroBaseline,
  visualArtifactIntroDone,
} from "./visual-artifact-reveal.ts";

afterEach(() => {
  clearVisualArtifactSessionMemory();
});

describe("visual artifact intro memory", () => {
  it("baselines hydrated producer artifact ids", () => {
    const messages: Message[] = [
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        tool_result: {
          tool_call_id: "c1",
          tool: "capture_page",
          content: "ok",
          visual: {
            id: "art-1",
            mime: "image/png",
            store_ref: true,
            source: "capture",
          },
        },
        created_at: "t",
        seq: 1,
      },
    ];
    noteVisualArtifactIntroBaseline(messages);
    expect(visualArtifactIntroDone("art-1")).toBe(true);
  });
});
