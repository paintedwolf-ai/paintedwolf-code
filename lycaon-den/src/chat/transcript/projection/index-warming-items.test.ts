import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { shouldShowIndexWarmingInTranscript } from "./index-warming-copy.ts";

function msg(overrides: Partial<Message>): Message {
  return {
    id: "m1",
    role: "assistant",
    content: "",
    created_at: new Date().toISOString(),
    ...overrides,
  } as Message;
}

const warmingMessage = msg({
  id: "w1",
  ord: 3,
  role: "system",
  kind: "index_warming",
  workflow_run_id: "run-web",
  content: "Warmed web index — 2 hosts, 3 pages · widget frobnicator",
  index_warming: {
    trigger: "declared_url",
    tier: "crawl",
    topic: "docs.example",
    hosts: ["docs.example", "news.example"],
    pages: 3,
  },
});

describe("index_warming transcript visibility", () => {
  it("shows only when verbose mode is on", () => {
    expect(shouldShowIndexWarmingInTranscript(false)).toBe(false);
    expect(shouldShowIndexWarmingInTranscript(true)).toBe(true);
    expect(
      createTranscriptDisplayProjector()([warmingMessage], undefined, {
        verboseMode: false,
        layout: "worker",
      })[0]!,
    ).toEqual([]);
  });

  it("maps warming messages to activity when verbose, never to a bubble", () => {
    const items = messagesToTranscriptItems([warmingMessage], { verboseMode: true });
    const warming = items.find((i) => i.kind === "index_warming");
    expect(warming?.kind).toBe("index_warming");
    if (warming?.kind !== "index_warming") return;
    expect(warming.meta.pages).toBe(3);
    expect(items.some((i) => i.kind === "assistant")).toBe(false);
    expect(items.some((i) => i.kind === "user")).toBe(false);
  });

  it("omits warming messages from parent chat when verbose mode is off", () => {
    const items = messagesToTranscriptItems([warmingMessage], { verboseMode: false });
    expect(items.some((i) => i.kind === "index_warming")).toBe(false);
  });

  it("visible unknown system rows render an attributed fallback", () => {
    const items = messagesToTranscriptItems([
      msg({ id: "s1", role: "system", content: "internal note" }),
    ]);
    expect(items).toMatchObject([
      {
        kind: "fallback",
        key: "s1",
        role: "system",
        text: "internal note",
      },
    ]);
  });

  it("requires kind and metadata together", () => {
    const metaOnly = messagesToTranscriptItems([
      msg({
        id: "meta-only",
        role: "system",
        index_warming: { trigger: "declared_url", pages: 1 },
      }),
    ]);
    expect(metaOnly.some((item) => item.kind === "index_warming")).toBe(false);

    const kindOnly = messagesToTranscriptItems(
      [
        msg({
          id: "kind-only",
          role: "system",
          kind: "index_warming",
        }),
      ],
      { verboseMode: true },
    );
    expect(kindOnly).toEqual([]);
  });

  it("rebuilds web research spans when verbose mode changes", () => {
    const webSearchAssistant = msg({
      id: "a1",
      ord: 1,
      role: "assistant",
      content: "",
      workflow_run_id: "run-web",
      tool_calls: [{ id: "ws1", name: "web_search", args: { query: "widgets" } }],
      created_at: "2026-01-01T00:00:01.000Z",
    });
    const webSearchResult = msg({
      id: "tr1",
      ord: 2,
      role: "tool",
      content: "ok",
      workflow_run_id: "run-web",
      tool_result: {
        assistant_message_id: "a1",
        tool_call_id: "ws1",
        tool: "web_search",
        content: "ok",
        outcome: "completed",
      },
      created_at: "2026-01-01T00:00:01.000Z",
    });
    const messages = [webSearchAssistant, webSearchResult, warmingMessage];

    const verbose = createTranscriptDisplayProjector()(messages, undefined, { verboseMode: true })[0]!;
    expect(verbose).toHaveLength(1);
    expect(verbose[0]?.kind).toBe("activity_span");
    if (verbose[0]?.kind === "activity_span") {
      expect(verbose[0].entries.map((e) => e.kind)).toEqual([
        "tool",
        "index_warming",
      ]);
    }

    const quiet = createTranscriptDisplayProjector()(messages, undefined, { verboseMode: false })[0]!;
    expect(quiet).toHaveLength(1);
    expect(quiet[0]?.kind).toBe("activity_span");
  });
});
