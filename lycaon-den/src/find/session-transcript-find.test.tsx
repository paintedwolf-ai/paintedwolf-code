import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { Message } from "../api/types.ts";
import { ChatSpanBlocks } from "../components/transcript/transcript-viewport-test-harness.tsx";
import { toolApprovalFixture } from "../chat/checkpoint/approval-test-fixtures.ts";
import { messagesToTranscriptItems } from "../chat/transcript/projection/transcript-items.ts";
import { fileEditPreviewFixture } from "../test/file-edit-fixture.ts";
import {
  findController,
  findNext,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "./find-controller.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";
import { FindBar } from "./FindBar.tsx";

afterEach(() => {
  resetFindControllerForTests();

  document.body.replaceChildren();
});

describe("session transcript findable adapter", () => {
  it("finds a unique needle across the resident chat transcript with next wrap", async () => {
    const messages = [
      {
        id: "u1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "please find UNIQUE_NEEDLE_ALPHA here",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "also UNIQUE_NEEDLE_ALPHA again",
        created_at: "2026-01-01T00:00:01Z",
      },
    ];

    const blocks = [
      {
        kind: "run" as const,
        runId: "ambient",
        ambientSpan: true,
        items: [
          { kind: "user" as const, key: "u1", text: messages[0]!.content },
          { kind: "assistant" as const, key: "a1", text: messages[1]!.content },
        ],
      },
    ];

    render(() => (
      <>
        <FindBar />
        <ChatSpanBlocks
          blocks={blocks}
          sessionId="s-find"
          messages={messages}
          workers={[]}
          visibleTurnActive={false}
        />
      </>
    ));

    expect(await screen.findByTestId("message-stream")).toBeTruthy();
    openFind();
    setFindQuery("UNIQUE_NEEDLE_ALPHA");
    expect(findController.matches().length).toBeGreaterThanOrEqual(2);
    expect(findController.activeIndex()).toBe(0);
    expect(
      document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length,
    ).toBeGreaterThanOrEqual(2);

    const before = findController.activeIndex();
    findNext();
    expect(findController.activeIndex()).not.toBe(before);

    fireEvent.click(screen.getByTestId("find-bar-close"));
    expect(document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
  });

  it("discovers matches inside closed Approvals and Files edited sections of an activity group and cleans up temporary disclosures", async () => {
    const messages: Message[] = [
      {
        id: "a-find",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "call-1", name: "command", args: { command: "write needle" } }],
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "cp-find-1",
        ord: 2,
        role: "system",
        kind: "checkpoint",
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        content: "",
        checkpoint_meta: {
          id: "cp-find-1",
          kind: "tool_approval",
          state: "approved",
          title: "Allow STACK_FIND_OMEGA primary gate",
          parent_message_id: "a-find",
          call_index: 0,
          tool_approval: toolApprovalFixture({
            command: "run STACK_FIND_OMEGA",
            title: "Allow STACK_FIND_OMEGA primary gate",
          }),
        },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "cp-find-2",
        ord: 3,
        role: "system",
        kind: "checkpoint",
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        content: "",
        checkpoint_meta: {
          id: "cp-find-2",
          kind: "tool_approval",
          state: "denied",
          title: "Allow secondary check",
          parent_message_id: "a-find",
          call_index: 0,
          tool_approval: toolApprovalFixture({
            command: "check secondary",
            title: "Allow secondary check",
          }),
        },
        created_at: "2026-01-01T00:00:02Z",
      },
      {
        id: "r-find-1",
        ord: 4,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "{}",
        tool_result: {
          assistant_message_id: "a-find",
          tool_call_id: "call-1",
          tool: "command",
          content: "{}",
          outcome: "completed",
          file_edit_previews: [
            fileEditPreviewFixture({
              path: "src/needle_target_file.ts",
              before: "const oldVal = 1;\n",
              after: "const STACK_FIND_OMEGA = 2;\n",
            }),
          ],
        },
        created_at: "2026-01-01T00:00:03Z",
      },
    ];
    const blocks = [
      {
        kind: "run" as const,
        runId: "ambient",
        ambientSpan: true,
        items: messagesToTranscriptItems(messages, { layout: "chat" }),
      },
    ];

    const { container } = render(() => (
      <>
        <FindBar />
        <ChatSpanBlocks
          blocks={blocks}
          sessionId="s-find-stack"
          messages={messages}
          workers={[]}
          visibleTurnActive={false}
        />
      </>
    ));

    expect(await screen.findByTestId("message-stream")).toBeTruthy();
    const approvalsToggle = container.querySelector<HTMLButtonElement>('[data-testid="activity-group-approvals-toggle"]');
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");

    openFind();
    setFindQuery("STACK_FIND_OMEGA");
    expect(findController.matches().length).toBeGreaterThanOrEqual(2);
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("true");

    findNext();
    const diffToggle = container.querySelector<HTMLButtonElement>('[data-testid="diff-group"] button.den-transcript-disclosure-toggle');
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("true");

    fireEvent.click(screen.getByTestId("find-bar-close"));
    expect(document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("false");
  });
});
