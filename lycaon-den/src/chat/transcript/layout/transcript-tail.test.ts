import { describe, expect, it } from "vitest";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import {
  transcriptDisplayTail,
  transcriptTailDelivery,
  isFirstProseDelivery,
} from "./transcript-tail.ts";

function user(key: string, text = "hi"): TranscriptItem {
  return { kind: "user", key, text };
}

function assistant(key: string, text = "ok"): TranscriptItem {
  return { kind: "assistant", key, text };
}

function feedback(key: string, answer?: string): TranscriptItem {
  return {
    kind: "workflow_feedback",
    key,
    meta: {
      phase_id: "intake",
      prompt: "Pick one",
      response_type: "single_choice",
      options: ["a", "b"],
      ...(answer !== undefined ? { answer } : {}),
    },
  };
}

function tool(
  key: string,
  status: "running" | "completed",
  output?: string,
): TranscriptItem {
  return {
    kind: "tool",
    key,
    part: {
      id: key,
      toolCallId: `call-${key}`,
      assistantMessageId: "a1",
      messageId: key,
      tool: "command",
      kind: "command",
      status,
      output,
    },
  };
}

describe("transcriptDisplayTail", () => {
  it("reads the last painted item across spans, not an empty trailing span", () => {
    expect(transcriptDisplayTail([])).toBeNull();
    expect(
      transcriptDisplayTail([{ items: [user("u1"), assistant("a1")] }]),
    ).toMatchObject({ key: "a1", kind: "assistant", itemCount: 2 });
    expect(
      transcriptDisplayTail([
        { items: [user("u1"), assistant("a1")] },
        { items: [] },
        { items: [feedback("q1")] },
      ]),
    ).toMatchObject({ key: "q1", kind: "workflow_feedback", itemCount: 3 });
  });
});

describe("transcriptTailDelivery", () => {
  it("treats only first nonempty text as an arrival, including a reused empty draft slot", () => {
    const tail = (text: string) => transcriptDisplayTail([{ items: [assistant("slot", text)] }]);
    expect(isFirstProseDelivery(null, tail(""))).toBe(false);
    expect(isFirstProseDelivery(tail(""), tail("Hello"))).toBe(true);
    expect(isFirstProseDelivery(tail("Hello"), tail("Hello world"))).toBe(false);
    expect(isFirstProseDelivery(tail("Hello"), tail("Hello"))).toBe(false);
    expect(isFirstProseDelivery(tail("Hello"), transcriptDisplayTail([{ items: [assistant("next")] }]))).toBe(true);
  });

  it("does not replay arrival after a transient empty update to readable text", () => {
    const initial = transcriptDisplayTail([{ items: [assistant("slot", "Readable")] }]);
    const empty = transcriptDisplayTail([{ items: [assistant("slot", "")] }], initial);
    const final = transcriptDisplayTail([{ items: [assistant("slot", "Readable again")] }], empty);
    expect(isFirstProseDelivery(empty, final)).toBe(false);
  });

  it("does not deliver prose again when grounding or status changes", () => {
    const item = assistant("a1", "Ready");
    const before = transcriptDisplayTail([{ items: [item] }]);
    const after = transcriptDisplayTail([{ items: [{ ...item, grounding: { traced: true, checks: [] } } as TranscriptItem] }]);
    expect(transcriptTailDelivery(before, after)).toBeNull();
  });

  it("treats host user echoes as structural arrivals", () => {
    const sent = transcriptDisplayTail([{ items: [user("u1")] }]);
    expect(transcriptTailDelivery(null, sent)).toBe("structural");
    expect(
      transcriptTailDelivery(
        transcriptDisplayTail([{ items: [user("u1")] }]),
        transcriptDisplayTail([{ items: [user("u1"), assistant("a1")] }]),
      ),
    ).toBe("prose");
    expect(
      transcriptTailDelivery(
        transcriptDisplayTail([{ items: [assistant("a1")] }]),
        transcriptDisplayTail([{ items: [assistant("a1"), user("u2")] }]),
      ),
    ).toBe("structural");
    // A prepend changes count without changing the tail.
    expect(
      transcriptTailDelivery(
        transcriptDisplayTail([{ items: [user("u-old")] }]),
        transcriptDisplayTail([{ items: [user("u-older"), user("u-old")] }]),
      ),
    ).toBe("structural");
    expect(
      transcriptTailDelivery(
        transcriptDisplayTail([{ items: [assistant("a-old")] }]),
        transcriptDisplayTail([
          { items: [user("u-older"), assistant("a-old")] },
        ]),
      ),
    ).toBe("structural");
  });

  it("delivers in-place prose without delivering in-place card state", () => {
    const initialProse = transcriptDisplayTail([{ items: [assistant("a1", "a")] }]);
    const nextProse = transcriptDisplayTail([{ items: [assistant("a1", "ab")] }]);
    expect(transcriptTailDelivery(initialProse, nextProse)).toBe("prose");

    const open = transcriptDisplayTail([{ items: [feedback("q1")] }]);
    const answered = transcriptDisplayTail([
      { items: [feedback("q1", "a")] },
    ]);
    expect(transcriptTailDelivery(open, answered)).toBeNull();
    expect(transcriptTailDelivery(answered, answered)).toBeNull();

    const running = transcriptDisplayTail([
      { items: [tool("tool-1", "running")] },
    ]);
    const completed = transcriptDisplayTail([
      { items: [tool("tool-1", "completed", "done")] },
    ]);
    expect(transcriptTailDelivery(running, completed)).toBeNull();
    expect(transcriptTailDelivery(completed, completed)).toBeNull();
  });

  it("delivers a newly appended structural card once", () => {
    const prose = transcriptDisplayTail([{ items: [assistant("a1")] }]);
    const withTool = transcriptDisplayTail([
      { items: [assistant("a1"), tool("tool-1", "running")] },
    ]);

    expect(transcriptTailDelivery(prose, withTool)).toBe("structural");
  });

  it("does not deliver a tail replacement or withdrawal", () => {
    const withTool = transcriptDisplayTail([
      { items: [assistant("a1"), tool("tool-1", "completed", "done")] },
    ]);
    const replaced = transcriptDisplayTail([
      { items: [assistant("a1"), feedback("q1")] },
    ]);
    const withdrawn = transcriptDisplayTail([{ items: [assistant("a1")] }]);

    expect(transcriptTailDelivery(withTool, replaced)).toBeNull();
    expect(transcriptTailDelivery(withTool, withdrawn)).toBeNull();
  });
});
