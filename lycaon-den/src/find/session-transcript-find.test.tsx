import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { Message } from "../api/types.ts";
import { ChatSpanBlocks } from "../components/transcript/transcript-viewport-test-harness.tsx";
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
        tool_calls: [
          { id: "call-1", name: "command", args: { command: "run gate-one" } },
          { id: "call-2", name: "command", args: { command: "check secondary" } },
        ],
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "r-find-1",
        ord: 2,
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
          checkpoint_decision: {
            checkpoint_id: "cp-find-1",
            kind: "tool_approval",
            status: "approved",
            tool: "command",
            subject: "STACK_FIND_OMEGA",
            grant_title: "Allow once",
          },
          file_edit_previews: [
            fileEditPreviewFixture({ path: "src/mod_a.ts", before: "a\n", after: "b\n" }),
            fileEditPreviewFixture({ path: "src/mod_b.ts", before: "a\n", after: "b\n" }),
            fileEditPreviewFixture({
              path: "src/STACK_FIND_OMEGA.ts",
              before: "const oldVal = 1;\n",
              after: "const nextVal = 2;\n",
            }),
          ],
        },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "r-find-2",
        ord: 3,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "denied",
        tool_result: {
          assistant_message_id: "a-find",
          tool_call_id: "call-2",
          tool: "command",
          content: "denied",
          outcome: "rejected",
          checkpoint_decision: {
            checkpoint_id: "cp-find-2",
            kind: "tool_approval",
            status: "rejected",
            tool: "command",
            subject: "secondary",
          },
        },
        created_at: "2026-01-01T00:00:02Z",
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
    const diffToggle = container.querySelector<HTMLButtonElement>('[data-testid="diff-group"] button.den-transcript-disclosure-toggle');
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("false");

    openFind();
    setFindQuery("STACK_FIND_OMEGA");
    const totalMatches = findController.matches().length;
    expect(totalMatches).toBeGreaterThanOrEqual(2);
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("false");

    let sawApprovalsExpanded = false;
    let sawDiffExpanded = false;
    for (let step = 0; step < 12 && (!sawApprovalsExpanded || !sawDiffExpanded); step++) {
      findNext();
      if (approvalsToggle?.getAttribute("aria-expanded") === "true") sawApprovalsExpanded = true;
      if (diffToggle?.getAttribute("aria-expanded") === "true") sawDiffExpanded = true;
    }
    expect(sawApprovalsExpanded).toBe(true);
    expect(sawDiffExpanded).toBe(true);

    fireEvent.click(screen.getByTestId("find-bar-close"));
    expect(document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("false");
  });
});
