import { stubClient } from "../../test/client-fixture.ts";
import {
  resetSessionTranscriptChatTest,

} from "./session-transcript-chat-test-harness.ts";

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { Message } from "../../api/types.ts";
import { noteTranscriptEntryBaseline } from "../../chat/transcript/presentation/transcript-entry.ts";
import { applyPreviewEvent } from "../../chat/visual/preview-store.ts";

describe("SessionTranscript preview and bubbles", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("keeps a final frame at its tool ord before later synthesis", () => {
    applyPreviewEvent({
      op: "frame",
      session_id: "s1",
      page_id: "drive:call-capture",
      assistant_message_id: "assistant-capture",
      tool_call_id: "call-capture",
      seq: 1,
      jpeg_b64: "final-frame",
    });
    applyPreviewEvent({
      op: "detach",
      session_id: "s1",
      page_id: "drive:call-capture",
      assistant_message_id: "assistant-capture",
      tool_call_id: "call-capture",
      seq: 2,
    });
    const client = stubClient({
      listSessionArtifacts: vi.fn(async () => ({ artifacts: [] })),
      watchPreview: vi.fn(async (
        _sessionId: string,
        req: { watching: boolean; page_id: string },
      ) => req),
    });
    const messages: Message[] = [
      {
        id: "assistant-capture",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [
          { id: "call-capture", name: "capture_page", args: { url: "https://example.test" } },
        ],
        ord: 46,
        created_at: "2026-08-14T00:00:00Z",
      },
      {
        id: "result-capture",
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "captured",
        tool_result: {
          tool_call_id: "call-capture",
          assistant_message_id: "assistant-capture",
          tool: "capture_page",
          content: "captured",
        },
        ord: 46,
        created_at: "2026-08-14T00:00:01Z",
      },
      {
        id: "assistant-synthesis",
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "Synthesis after capture",
        ord: 50,
        created_at: "2026-08-14T00:00:02Z",
      },
    ];


    render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={messages}
        checkpointClient={client}
      />
    ));

    const finalFrame = screen.getByLabelText("Final frame from live tool session");
    const synthesis = screen.getByText("Synthesis after capture");
    expect(finalFrame.compareDocumentPosition(synthesis) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  it("renders assistant markdown in left assistant bubble", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "**Project status** summary",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(container.querySelector(".bubble--assistant")).toBeTruthy();
    expect(container.querySelector(".bubble--user")).toBeNull();
    expect(screen.getByText("Project status").tagName).toBe("STRONG");
  });

  it("renders hydrated user prompts without replaying entry fade", () => {
    noteTranscriptEntryBaseline("s1", ["u1"]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          {
            id: "u1",
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content: "hello there",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    const userBubble = container.querySelector(".bubble--user");
    expect(userBubble?.textContent).toContain("hello there");
    expect(userBubble?.classList.contains("den-enter-fade")).toBe(false);
  });

  it("renders attachment chips from content_parts instead of raw fences", () => {
    noteTranscriptEntryBaseline("s1", ["u1"]);
    const fence =
      '```attachment filename="browser_nested_link_clicks.js" mime="text/plain" truncated="false"\n' +
      "[User attached file: browser/actors/test/browser/browser_nested_link_clicks.js]\n```";
    const { container, getByTestId } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        projectId="p1"
        rootRefs={[{ id: "root-a", path: "/proj", is_primary: true }]}
        messages={[
          {
            id: "u1",
            role: "user",
            origin: "user" as const,
            authority: "user" as const,
            trust_tier: "trusted" as const,
            content: `Tell me how this works\n\n${fence}`,
            content_parts: [
              {
                content: "Tell me how this works",
                origin: "user",
                authority: "user",
                trust_tier: "trusted",
              },
              {
                content: fence,
                origin: "retrieval",
                authority: "none",
                trust_tier: "untrusted",
                reference_kind: "path_file",
                path: "browser/actors/test/browser/browser_nested_link_clicks.js",
                media_type: "text/plain",
              },
            ],
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    const userBubble = container.querySelector(".bubble--user");
    expect(userBubble?.textContent).toContain("Tell me how this works");
    expect(userBubble?.textContent).not.toContain("```attachment");
    expect(getByTestId("transcript-attachment-chip").textContent).toContain(
      "browser_nested_link_clicks.js",
    );
    fireEvent.contextMenu(getByTestId("transcript-attachment-chip"));
    expect(screen.getByTestId("path-menu-reveal-tree")).toBeTruthy();
  });

  it("renders fresh user prompts without entry fade", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          {
            id: "u-new",
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content: "new prompt",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    const userBubble = container.querySelector(".bubble--user");
    expect(userBubble?.textContent).toContain("new prompt");
    expect(userBubble?.classList.contains("den-enter-fade")).toBe(false);
  });

});
