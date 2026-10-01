import { describe, it, expect } from "vitest";
import type { Message } from "../../../api/types.ts";
import { allMessageKinds } from "../../../api/enum-registries.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";

describe("transcript-items", () => {
  it("projects every registered message kind", () => {
    for (const kind of allMessageKinds()) {
      const message: Message = {
        id: `kind-${kind}`,
        role: kind === "draft" ? "assistant" : "system",
        origin: kind === "draft" ? "model" : "host",
        authority: kind === "draft" ? "none" : "system",
        trust_tier: "trusted",
        kind,
        content: `visible ${kind}`,
        created_at: "t",
      };
      if (kind === "index_warming") {
        message.index_warming = { trigger: "declared_url" };
      }
      if (kind === "workflow_explain") {
        message.workflow_explain = { phase_id: "ingest", summary: "A scan runs first", body: "Why." };
      }
      const items = messagesToTranscriptItems([message], { verboseMode: true });
      expect(
        items.some((item) => item.key === message.id),
        `missing projection for ${kind}`,
      ).toBe(true);
    }
  });

  it("projects a phase note as its own chicklet row in its run", () => {
    const meta = {
      phase_id: "ingest",
      summary: "A full security scan runs first",
      body: "Why.",
      progress: { full_pass: { assessment_id: "pass-1", project_id: "project-1" } },
    };
    const note = {
      id: "explain-1",
      role: "system",
      origin: "host",
      kind: "workflow_explain",
      visibility: "transcript",
      workflow_run_id: "run-1",
      workflow_explain: meta,
      content: meta.summary,
      created_at: "t",
    } as unknown as Message;
    expect(messagesToTranscriptItems([note])).toEqual([
      { kind: "workflow_explain", key: "explain-1", meta, runId: "run-1" },
    ]);
  });

  it("projects a message sent into the running turn as an ordinary prompt row", () => {
    const messages = [
      { id: "u1", role: "user", content: "count to four", created_at: "t1" },
      {
        id: "u2",
        role: "user",
        kind: "user_continuation",
        content: "stop - say ALPHA",
        created_at: "t2",
      },
    ] as unknown as Message[];
    const items = messagesToTranscriptItems(messages);
    const users = items.filter((i) => i.kind === "user");
    // A landed continuation reads like any other prompt.
    expect(users).toEqual([
      { kind: "user", key: "u1", text: "count to four" },
      { kind: "user", key: "u2", text: "stop - say ALPHA" },
    ]);
  });

  it("does not render tool calls from a withdrawn proposal", () => {
    const messages = [
      { id: "u1", role: "user", content: "go", created_at: "t1" },
      {
        id: "a1",
        role: "assistant",
        kind: "draft",
        draft_status: "withdrawn",
        content: "",
        tool_calls: [{ id: "call_dead", name: "command" }],
        created_at: "t2",
      },
    ] as unknown as Message[];
    // A withdrawn call has no result because execution was canceled.
    const items = messagesToTranscriptItems(messages, { verboseMode: true });
    expect(items.filter((i) => i.kind === "tool")).toHaveLength(0);
  });

  it("still renders tool calls from a live proposal awaiting its result", () => {
    const messages = [
      { id: "u1", role: "user", content: "go", created_at: "t1" },
      {
        id: "a1",
        role: "assistant",
        content: "",
        tool_calls: [{ id: "call_live", name: "command" }],
        created_at: "t2",
      },
    ] as unknown as Message[];
    const items = messagesToTranscriptItems(messages, { verboseMode: true });
    expect(items.filter((i) => i.kind === "tool")).toHaveLength(1);
  });
});
