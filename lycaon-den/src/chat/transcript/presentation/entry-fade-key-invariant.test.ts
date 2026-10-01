import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "../projection/transcript-items.ts";
import { createTranscriptDisplayProjector } from "../projection/transcript-display-projection.ts";
import {
  activitySpanEnterFadeKey,
  transcriptEntryKeysFromMessages,
  workerGroupEnterFadeKey,
} from "./transcript-entry.ts";

/** Row identity distinguishes repeated tool-call IDs across turns. */

const TOOL_WIRE_ID = "call_0";

function toolFixture(): Message[] {
  return [
    {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [{ id: TOOL_WIRE_ID, name: "read", args: { path: "a.go" } }],
      created_at: "t",
      seq: 1,
    },
    {
      id: "t1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      tool_result: {
        assistant_message_id: "a1",
        tool_call_id: TOOL_WIRE_ID,
        tool: "read",
        content: "",
      },
      content: "ok",
      created_at: "t",
      seq: 2,
    },
    {
      id: "u1",
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: "hi",
      created_at: "t",
      seq: 3,
    },
    {
      id: "a2",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "done",
      visibility: "transcript",
      created_at: "t",
      seq: 4,
    },
  ];
}

function collectItemKeys(messages: readonly Message[]): Set<string> {
  const keys = new Set<string>();
  for (const item of createTranscriptDisplayProjector()(messages, undefined)[0]!) {
    keys.add(item.key);
    if (item.kind === "worker_group") {
      keys.add(workerGroupEnterFadeKey(item.key));
      for (const part of item.parts) keys.add(part.id);
    }
    if (item.kind === "activity_span") {
      keys.add(activitySpanEnterFadeKey(item.key));
      for (const entry of item.entries) {
        if (entry.kind === "tool") keys.add(entry.part.id);
      }
    }
  }
  return keys;
}

describe("entry fade key invariant", () => {
  it("baselines only transcript item keys — never bare wire tool_call_id", () => {
    const messages = toolFixture();
    const baseline = transcriptEntryKeysFromMessages(messages);
    const itemKeys = collectItemKeys(messages);

    expect(baseline).toContain("a1:call_0");
    expect(baseline).not.toContain(TOOL_WIRE_ID);
    for (const key of baseline) {
      expect(itemKeys.has(key)).toBe(true);
    }
  });

  it("tool transcript rows use stable part.id equal to item.key", () => {
    const items = messagesToTranscriptItems(toolFixture(), { layout: "chat" });
    const tools = items.filter((item) => item.kind === "tool");
    expect(tools.length).toBeGreaterThan(0);
    for (const item of tools) {
      if (item.kind !== "tool") continue;
      expect(item.key).toBe(item.part.id);
      expect(item.key).toMatch(/^[^:]+:call_0$/);
    }
  });
});
