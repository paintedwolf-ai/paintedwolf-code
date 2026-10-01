import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import {
  advanceTranscriptAnnouncement,
  type TranscriptAnnouncementState,
} from "./transcript-live-announcement.ts";

function assistant(
  id: string,
  content: string,
  status?: Message["status"],
): Message {
  return {
    id,
    role: "assistant",
    origin: "model",
    authority: "none",
    trust_tier: "trusted",
    content,
    status,
    created_at: "t",
  };
}

function step(
  previous: TranscriptAnnouncementState | undefined,
  messages: readonly Message[],
  sessionId = "s1",
) {
  return advanceTranscriptAnnouncement(previous, sessionId, messages);
}

describe("advanceTranscriptAnnouncement", () => {
  it("seeds hydrated history without announcing it", () => {
    const result = step(undefined, [assistant("a1", "History")]);
    expect(result.announcement).toBeUndefined();
  });

  it("announces an appended completed assistant turn", () => {
    const initial = step(undefined, []);
    const result = step(initial.state, [assistant("a1", "Done", "complete")]);
    expect(result.announcement).toBe("Assistant: Done");
  });

  it("waits for a streaming assistant turn to complete", () => {
    const initial = step(undefined, []);
    const streaming = step(initial.state, [
      assistant("a1", "Part", "streaming"),
    ]);
    expect(streaming.announcement).toBeUndefined();

    const complete = step(streaming.state, [
      assistant("a1", "Complete answer", "complete"),
    ]);
    expect(complete.announcement).toBe("Assistant: Complete answer");

    const stable = step(complete.state, [
      assistant("a1", "Complete answer", "complete"),
    ]);
    expect(stable.announcement).toBeUndefined();
  });

  it("ignores prepended history and internal messages", () => {
    const visible = assistant("a2", "Current");
    const initial = step(undefined, [visible]);
    const internal = {
      ...assistant("hidden", "Hidden"),
      visibility: "internal" as const,
    };
    const result = step(initial.state, [assistant("a1", "Older"), internal, visible]);
    expect(result.announcement).toBeUndefined();
  });

  it("announces a newly written phase note once, and never from history", () => {
    const note: Message = {
      id: "explain",
      role: "system",
      origin: "host",
      authority: "none",
      trust_tier: "trusted",
      kind: "workflow_explain",
      content: "A full security scan runs first",
      workflow_explain: { phase_id: "ingest", summary: "A full security scan runs first", body: "Why." },
      created_at: "t",
    };
    const hydrated = step(undefined, [note]);
    expect(hydrated.announcement).toBeUndefined();

    const initial = step(undefined, []);
    const appended = step(initial.state, [note]);
    expect(appended.announcement).toBe("A full security scan runs first");
    expect(step(appended.state, [note]).announcement).toBeUndefined();
  });

  it("seeds a new session without announcing its history", () => {
    const first = step(undefined, [assistant("a1", "First")], "s1");
    const next = step(first.state, [assistant("a2", "Second")], "s2");
    expect(next.announcement).toBeUndefined();
  });
});
