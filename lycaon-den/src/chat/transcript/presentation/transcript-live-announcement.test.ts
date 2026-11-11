import { afterEach, describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import {
  advanceTranscriptAnnouncement,
  clearTranscriptAnnouncementMemory,
  isCommittedAssistantReply,
  noteTranscriptAnnouncementBaseline,
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
  afterEach(() => {
    clearTranscriptAnnouncementMemory();
  });

  it("seeds hydrated history without announcing it", () => {
    const result = step(undefined, [assistant("a1", "History")]);
    expect(result.announcement).toBeUndefined();
  });

  it("announces an appended completed assistant turn", () => {
    const initial = step(undefined, []);
    const result = step(initial.state, [assistant("a1", "Done", "complete")]);
    expect(result.announcement).toBe("Assistant: Done");
  });

  it.each([
    ["The limit is in [a.go:42-58](a.go#L42-L58).", "The limit is in a.go:42-58."],
    ["See [my\\_file.go:1](<my_file v2.go#L1>).", "See my_file.go:1."],
    ["Read [RFC 9110](https://example.test/spec) and [x].", "Read RFC 9110 and [x]."],
  ])("speaks a link's label and not its destination: %s", (content, spoken) => {
    const initial = step(undefined, []);
    const result = step(initial.state, [assistant("a1", content, "complete")]);
    expect(result.announcement).toBe(`Assistant: ${spoken}`);
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

  it("announces a reply the host writes as a hidden draft once it commits", () => {
    const user: Message = {
      id: "u2",
      role: "user",
      origin: "user",
      authority: "user",
      trust_tier: "trusted",
      content: "Next",
      created_at: "t",
    };
    const history = [assistant("a1", "Earlier", "complete")];
    const draft = (content: string): Message => ({
      ...assistant("a2", content),
      visibility: "internal",
      draft_status: "live",
    });
    const committed: Message = {
      ...assistant("a2", "Answer"),
      kind: "completion_report",
      draft_status: "committed",
    };

    const seeded = step(undefined, history);
    const asked = step(seeded.state, [...history, user]);
    const opened = step(asked.state, [...history, user, draft("")]);
    const written = step(opened.state, [...history, user, draft("Answer")]);
    expect([asked, opened, written].map((s) => s.announcement)).toEqual([
      undefined,
      undefined,
      undefined,
    ]);

    const done = step(written.state, [...history, user, committed]);
    expect(done.announcement).toBe("Assistant: Answer");
    const settled = step(done.state, [...history, user, { ...committed, status: "complete" }]);
    expect(settled.announcement).toBeUndefined();
  });

  it("announces nothing when a reload empties and refills the history", () => {
    const history = [assistant("a1", "First", "complete"), assistant("a2", "Second", "complete")];
    const seeded = step(undefined, history);
    const emptied = step(seeded.state, []);
    expect(emptied.announcement).toBeUndefined();
    const refilled = step(emptied.state, history);
    expect(refilled.announcement).toBeUndefined();
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

  it("re-seeds baseline when a restored session hydrates a completed assistant reply after a stale snapshot", () => {
    const user: Message = {
      id: "u1",
      role: "user",
      origin: "user",
      authority: "user",
      trust_tier: "trusted",
      content: "Prompt before reload",
      created_at: "t",
    };
    const restoredReply = assistant("a1", "Committed before reload", "complete");

    noteTranscriptAnnouncementBaseline("s1", [user]);
    const fromStaleSnapshot = step(undefined, [user], "s1");
    expect(fromStaleSnapshot.announcement).toBeUndefined();

    noteTranscriptAnnouncementBaseline("s1", [user, restoredReply]);
    const hydrated = step(fromStaleSnapshot.state, [user, restoredReply], "s1");
    expect(hydrated.announcement).toBeUndefined();

    const liveFollowUp = step(
      hydrated.state,
      [user, restoredReply, assistant("a2", "Live reply", "complete")],
      "s1",
    );
    expect(liveFollowUp.announcement).toBe("Assistant: Live reply");
  });

  it("identifies committed assistant replies for snapshot persistence", () => {
    expect(isCommittedAssistantReply(assistant("a1", "Ready", "complete"))).toBe(true);
    expect(isCommittedAssistantReply(assistant("a1", "Streaming", "streaming"))).toBe(false);
    expect(isCommittedAssistantReply(assistant("a1", "   ", "complete"))).toBe(false);
    expect(
      isCommittedAssistantReply({
        ...assistant("a1", "Draft"),
        visibility: "internal",
        draft_status: "live",
      }),
    ).toBe(false);
    expect(
      isCommittedAssistantReply({
        id: "u1",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "User prompt",
        created_at: "t",
      }),
    ).toBe(false);
  });
});
